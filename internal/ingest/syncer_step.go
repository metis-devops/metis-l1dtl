package ingest

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sort"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

type scanWindow struct {
	before    store.State
	from, end uint64
	target    uint64
	endHash   common.Hash
	ctc       common.Address
}

func (s *Syncer) Step(ctx context.Context) (bool, error) {
	if _, err := s.Status.Snapshot(); err != nil {
		return false, err
	}
	w, done, err := s.prepareWindow(ctx)
	if err != nil || done {
		return done, err
	}
	ready, err := s.initializeCTC(ctx, &w)
	if err != nil || !ready {
		return false, err
	}
	if err := s.anchorWindow(ctx, &w); err != nil {
		return false, err
	}
	logs, err := s.loadLogs(ctx, w)
	if err != nil {
		return false, err
	}
	records, ctc, err := s.decodeLogs(logs, w.ctc, w.from, w.end)
	if err != nil {
		return false, err
	}
	if err := s.commitWindow(ctx, w, ctc, records); err != nil {
		return false, err
	}
	caught := w.end == w.target
	s.Status.Set(caught, nil)
	slog.Info("L1 scan committed", "height", w.end, "enqueues", len(records), "caught_up", caught)
	return caught, nil
}

func (s *Syncer) prepareWindow(ctx context.Context) (scanWindow, bool, error) {
	before, err := s.Store.State()
	if err != nil {
		return scanWindow{}, false, fmt.Errorf("%w: %w", store.ErrIntegrity, err)
	}
	if before.Height != nil {
		h, e := s.header(ctx, *before.Height)
		if e != nil {
			return scanWindow{}, false, e
		}
		if h.Hash() != before.Hash {
			return scanWindow{}, false, fmt.Errorf("%w: committed block %d changed", ErrFatal, *before.Height)
		}
	}
	tip, err := s.RPC.HeaderByNumber(ctx, nil)
	if err != nil {
		return scanWindow{}, false, err
	}
	if tip == nil || tip.Number == nil || !tip.Number.IsUint64() {
		return scanWindow{}, false, fmt.Errorf("%w: invalid tip", ErrFatal)
	}
	target := uint64(0)
	if tip.Number.Uint64() >= s.Config.Confirmations {
		target = tip.Number.Uint64() - s.Config.Confirmations
	}
	from := s.Config.Start
	if before.Height != nil {
		if *before.Height >= target {
			s.Status.Set(true, nil)
			return scanWindow{}, true, nil
		}
		from = *before.Height + 1
	}
	if from > target {
		return scanWindow{}, true, nil
	}
	s.Status.Set(false, nil)
	end := target
	if target-from >= s.Config.BatchSize {
		end = from + s.Config.BatchSize - 1
	}
	return scanWindow{before: before, from: from, end: end, target: target, ctc: before.CTC}, false, nil
}

// Trust RPC log provenance; only anchor the window end before and after scanning.
func (s *Syncer) anchorWindow(ctx context.Context, w *scanWindow) error {
	h, err := s.header(ctx, w.end)
	if err != nil {
		return err
	}
	w.endHash = h.Hash()
	return nil
}

func (s *Syncer) loadLogs(ctx context.Context, w scanWindow) ([]types.Log, error) {
	changes, err := s.RPC.FilterLogs(ctx, ethereum.FilterQuery{FromBlock: number(w.from), ToBlock: number(w.end), Addresses: []common.Address{s.Config.AddressManager}, Topics: [][]common.Hash{{ContractABI.Events["AddressSet"].ID}, {nameHash}}})
	if err != nil {
		return nil, err
	}
	addresses := map[common.Address]bool{}
	if w.ctc != (common.Address{}) {
		addresses[w.ctc] = true
	}
	for _, l := range changes {
		if err := validateLog(l, w.from, w.end); err != nil {
			return nil, err
		}
		if l.Address != s.Config.AddressManager || len(l.Topics) != 2 || l.Topics[0] != ContractABI.Events["AddressSet"].ID || l.Topics[1] != nameHash {
			return nil, fmt.Errorf("%w: unexpected address log", ErrFatal)
		}
		v, err := unpack("AddressSet", l.Data)
		if err != nil {
			return nil, err
		}
		if a := v[0].(common.Address); a != (common.Address{}) {
			addresses[a] = true
		}
	}
	logs := append([]types.Log{}, changes...)
	if len(addresses) == 0 {
		return logs, nil
	}
	as := make([]common.Address, 0, len(addresses))
	for a := range addresses {
		as = append(as, a)
	}
	enqueues, err := s.RPC.FilterLogs(ctx, ethereum.FilterQuery{FromBlock: number(w.from), ToBlock: number(w.end), Addresses: as, Topics: [][]common.Hash{{ContractABI.Events["TransactionEnqueued"].ID}}})
	if err != nil {
		return nil, err
	}
	return append(logs, enqueues...), nil
}

func (s *Syncer) decodeLogs(logs []types.Log, ctc common.Address, from, end uint64) ([]store.Record, common.Address, error) {
	sort.Slice(logs, func(i, j int) bool {
		a, b := logs[i], logs[j]
		if a.BlockNumber != b.BlockNumber {
			return a.BlockNumber < b.BlockNumber
		}
		if a.TxIndex != b.TxIndex {
			return a.TxIndex < b.TxIndex
		}
		return a.Index < b.Index
	})
	var records []store.Record
	seen := map[string]types.Log{}
	for _, l := range logs {
		if err := validateLog(l, from, end); err != nil {
			return nil, ctc, err
		}
		id := fmt.Sprintf("%s/%d", l.BlockHash, l.Index)
		if old, ok := seen[id]; ok {
			if old.BlockNumber != l.BlockNumber || old.Address != l.Address || old.TxHash != l.TxHash || old.TxIndex != l.TxIndex || !bytes.Equal(old.Data, l.Data) || !equalTopics(old.Topics, l.Topics) {
				return nil, ctc, fmt.Errorf("%w: conflicting log", ErrFatal)
			}
			continue
		}
		seen[id] = l
		if l.Address == s.Config.AddressManager && l.Topics[0] == ContractABI.Events["AddressSet"].ID {
			v, err := unpack("AddressSet", l.Data)
			if err != nil {
				return nil, ctc, err
			}
			if v[1].(common.Address) != ctc {
				return nil, ctc, fmt.Errorf("%w: address history mismatch", ErrFatal)
			}
			ctc = v[0].(common.Address)
			continue
		}
		if l.Address != ctc {
			continue
		}
		r, chain, err := Decode(l)
		if err != nil {
			return nil, ctc, err
		}
		if chain.Cmp(number(s.Config.L2ChainID)) == 0 {
			records = append(records, r)
		}
	}
	return records, ctc, nil
}

func (s *Syncer) commitWindow(ctx context.Context, w scanWindow, ctc common.Address, records []store.Record) error {
	if w.before.Height == nil {
		if err := s.checkBootstrap(ctx); err != nil {
			return err
		}
	}
	if w.end == w.target && ctc == (common.Address{}) {
		return fmt.Errorf("%w: no active CanonicalTransactionChain at confirmed tip", ErrFatal)
	}
	h, err := s.header(ctx, w.end)
	if err != nil {
		return err
	}
	if h.Hash() != w.endHash {
		return fmt.Errorf("%w: scan changed before commit", ErrFatal)
	}
	if w.before.Height != nil {
		h, err = s.header(ctx, *w.before.Height)
		if err != nil {
			return err
		}
		if h.Hash() != w.before.Hash {
			return fmt.Errorf("%w: checkpoint changed before commit", ErrFatal)
		}
	}
	after := store.State{Height: &w.end, Hash: w.endHash, CTC: ctc}
	return s.Status.commitIfHealthy(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.Store.Commit(w.before, after, records); err != nil {
			return fmt.Errorf("%w: %w", store.ErrIntegrity, err)
		}
		return nil
	})
}
