package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"sort"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Bootstrap progress is deliberately not a deposit checkpoint. A restart before
// the first atomic scan commit repeats discovery; transient failures retry only
// the current page. Each Step processes at most one page under Run's timeout.
type bootstrapState struct {
	anchor common.Hash
	next   uint64
	ready  bool
	ctc    common.Address
}

func (s *Syncer) checkBootstrap(ctx context.Context) error {
	b := s.bootstrap
	if b == nil {
		return nil
	}
	h, err := s.header(ctx, s.Config.Start-1)
	if err != nil {
		return err
	}
	if h.Hash() != b.anchor {
		return fmt.Errorf("%w: initialization anchor changed", ErrFatal)
	}
	return nil
}

func (s *Syncer) initializeCTC(ctx context.Context, w *scanWindow) (bool, error) {
	if w.before.Height != nil {
		s.bootstrap = nil
		return true, nil
	}
	if w.from == 0 {
		return true, nil
	}
	s.Status.Set(false, nil)
	if s.bootstrap == nil {
		h, err := s.header(ctx, w.from-1)
		if err != nil {
			return false, err
		}
		s.bootstrap = &bootstrapState{anchor: h.Hash(), next: w.from - 1}
	}
	if err := s.checkBootstrap(ctx); err != nil {
		return false, err
	}
	b := s.bootstrap
	if b.ready {
		w.ctc = b.ctc
		return true, nil
	}
	page, err := s.loadBootstrapPage(ctx, b.next)
	if err != nil {
		return false, err
	}
	ctc, err := s.decodeBootstrapPage(page)
	if err != nil {
		return false, err
	}
	// Keep failed-page progress unpublished, including on timeout.
	if err := s.checkBootstrap(ctx); err != nil {
		return false, err
	}
	if len(page.logs) == 0 && page.from > 0 {
		end := b.next
		b.next = page.from - 1
		slog.Info("initial CTC history scanned", "from", page.from, "to", end, "next", b.next)
		return false, nil
	}
	b.ctc, b.ready = ctc, true
	w.ctc = ctc
	return true, nil
}

// A page remains local until its logs and bootstrap anchor have
// all been checked. Loading or decoding it must not advance bootstrap progress.
type bootstrapPage struct {
	from, end uint64
	logs      []types.Log
}

func (s *Syncer) loadBootstrapPage(ctx context.Context, end uint64) (bootstrapPage, error) {
	if s.Config.BatchSize == 0 {
		return bootstrapPage{}, fmt.Errorf("batch size must be positive")
	}
	from := uint64(0)
	if end >= s.Config.BatchSize {
		from = end - s.Config.BatchSize + 1
	}
	logs, err := s.RPC.FilterLogs(ctx, ethereum.FilterQuery{
		FromBlock: number(from), ToBlock: number(end), Addresses: []common.Address{s.Config.AddressManager},
		Topics: [][]common.Hash{{ContractABI.Events["AddressSet"].ID}, {nameHash}},
	})
	if err != nil {
		return bootstrapPage{}, fmt.Errorf("initial CTC logs [%d,%d]: %w", from, end, err)
	}
	for _, l := range logs {
		if err := validateLog(l, from, end); err != nil {
			return bootstrapPage{}, err
		}
		if l.Address != s.Config.AddressManager || len(l.Topics) != 2 || l.Topics[0] != ContractABI.Events["AddressSet"].ID || l.Topics[1] != nameHash {
			return bootstrapPage{}, fmt.Errorf("%w: unexpected initialization log", ErrFatal)
		}
		if _, err := unpack("AddressSet", l.Data); err != nil {
			return bootstrapPage{}, err
		}
	}
	return bootstrapPage{from: from, end: end, logs: logs}, nil
}

func (s *Syncer) decodeBootstrapPage(page bootstrapPage) (common.Address, error) {
	logs := page.logs
	if len(logs) == 0 {
		return common.Address{}, nil
	}
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
	vals, err := unpack("AddressSet", logs[0].Data)
	if err != nil {
		return common.Address{}, err
	}
	// The oldest event in this page can have a nonzero predecessor outside
	// the page. Its old address seeds only this page's transition checks.
	initial := vals[1].(common.Address)
	if page.from == 0 {
		initial = common.Address{}
	}
	_, ctc, err := s.decodeLogs(logs, initial, page.from, page.end)
	return ctc, err
}
