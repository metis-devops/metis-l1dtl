package blobingest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/metis-devops/metis-l1dtl/internal/blob"
	"github.com/metis-devops/metis-l1dtl/internal/config"
	"github.com/metis-devops/metis-l1dtl/internal/ingest"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

type RPC interface {
	HeaderByNumber(context.Context, *big.Int) (*types.Header, error)
	BlockByNumber(context.Context, *big.Int) (*types.Block, error)
	BlockByHash(context.Context, common.Hash) (*types.Block, error)
	TransactionReceipt(context.Context, common.Hash) (*types.Receipt, error)
	FilterLogs(context.Context, ethereum.FilterQuery) ([]types.Log, error)
}
type BlobSource interface {
	Frames(context.Context, *types.Header, []common.Hash) ([]store.Frame, error)
}
type Syncer struct {
	Config    config.Config
	RPC       RPC
	Beacon    BlobSource
	Store     *store.Store
	Status    *ingest.Status
	bootstrap *bootstrap
	backfills map[common.Address]*senderBackfill
}
type bootstrap struct {
	next   uint64
	anchor common.Hash
	auth   *authorization
	ready  bool
}

func number(n uint64) *big.Int   { return new(big.Int).SetUint64(n) }
func fatal(message string) error { return fmt.Errorf("%w: %s", ingest.ErrFatal, message) }
func (s *Syncer) header(ctx context.Context, n uint64) (*types.Header, error) {
	h, err := s.RPC.HeaderByNumber(ctx, number(n))
	if err != nil {
		return nil, err
	}
	if h == nil || h.Number == nil || !h.Number.IsUint64() || h.Number.Uint64() != n {
		return nil, fatal("invalid Blob scan header")
	}
	return h, nil
}
func (s *Syncer) check(ctx context.Context, n uint64, hash common.Hash) error {
	h, err := s.header(ctx, n)
	if err != nil {
		return err
	}
	if h.Hash() != hash {
		return fatal("Blob scan anchor changed")
	}
	return nil
}
func (s *Syncer) checkpoint(ctx context.Context, st store.BlobState) error {
	if st.RetentionHeight != nil {
		if err := s.check(ctx, *st.RetentionHeight, st.RetentionHash); err != nil {
			return err
		}
	}
	if st.Height == nil {
		return nil
	}
	return s.check(ctx, *st.Height, st.Hash)
}
func (s *Syncer) Run(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-s.Status.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	for ctx.Err() == nil {
		stepCtx, stop := context.WithTimeout(ctx, 5*time.Minute)
		caught, err := s.Step(stepCtx)
		stop()
		if err != nil {
			s.Status.SetBlobReady(false)
			if errors.Is(err, ingest.ErrFatal) || errors.Is(err, store.ErrIntegrity) || errors.Is(err, blob.ErrInvalid) {
				s.Status.Set(false, err)
				slog.Error("Blob sync stopped", "error", err)
				return
			}
			if ctx.Err() != nil {
				return
			}
			// RPC diagnostics may contain credentials or provider-specific payloads.
			slog.Warn("Blob scan failed; retrying")
		}
		if err == nil && !caught {
			continue
		}
		timer := time.NewTimer(s.Config.Poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

func (s *Syncer) commit(ctx context.Context, before, after store.BlobState, txs []store.BlobTransaction, channels []store.BlobChannel) error {
	return s.Status.CommitIfHealthy(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.Store.CommitBlob(before, after, txs, channels); err != nil {
			return fmt.Errorf("%w: %w", store.ErrIntegrity, err)
		}
		return nil
	})
}
func (s *Syncer) Step(ctx context.Context) (bool, error) {
	if _, err := s.Status.Snapshot(); err != nil {
		return false, err
	}
	st, target, err := s.prepare(ctx)
	if err != nil {
		return false, err
	}
	caught := st.Height != nil && *st.Height >= target
	if caught || s.Config.InboxStart > target {
		s.Status.SetBlobReady(true)
		return true, nil
	}
	a, ready, err := s.initialize(ctx, st)
	if errors.Is(err, errBackfillPending) {
		return false, nil
	}
	if err != nil || !ready {
		return false, err
	}
	from := s.Config.InboxStart
	if st.Height != nil {
		from = *st.Height + 1
	}
	end := target
	if target-from >= s.Config.BatchSize {
		end = from + s.Config.BatchSize - 1
	}
	anchor, err := s.header(ctx, end)
	if err != nil {
		return false, err
	}
	logs, err := s.authLogs(ctx, a, from, end)
	if errors.Is(err, errBackfillPending) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	txs, channels, err := s.scan(ctx, a, logs, from, end, st.Cutoff)
	if err != nil {
		return false, err
	}
	if err := s.check(ctx, end, anchor.Hash()); err != nil {
		return false, err
	}
	if err := s.checkpoint(ctx, st); err != nil {
		return false, err
	}
	if st.Height == nil && s.Config.InboxStart > 0 {
		if err := s.check(ctx, s.Config.InboxStart-1, s.bootstrap.anchor); err != nil {
			return false, err
		}
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return false, err
	}
	after := st
	after.Height = &end
	after.Hash = anchor.Hash()
	after.Auth = raw
	if err := s.commit(ctx, st, after, txs, channels); err != nil {
		return false, err
	}
	s.bootstrap = nil
	s.backfills = nil
	caught = end == target
	s.Status.SetBlobReady(caught)
	slog.Info("Blob scan committed", "height", end, "transactions", len(txs), "channels", len(channels), "cutoff", st.Cutoff, "caught_up", caught)
	return caught, nil
}
func cloneAuth(a *authorization) (*authorization, error) {
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	return decodeAuth(raw)
}
func (s *Syncer) initialize(ctx context.Context, st store.BlobState) (*authorization, bool, error) {
	if st.Height != nil {
		a, err := decodeAuth(st.Auth)
		if len(st.Auth) == 0 {
			return nil, false, fatal("missing authorization checkpoint")
		}
		return a, true, err
	}
	if s.Config.InboxStart == 0 {
		return newAuthorization(), true, nil
	}
	end := s.Config.InboxStart - 1
	if s.bootstrap == nil {
		h, err := s.header(ctx, end)
		if err != nil {
			return nil, false, err
		}
		s.bootstrap = &bootstrap{anchor: h.Hash(), auth: newAuthorization()}
	}
	b := s.bootstrap
	if err := s.check(ctx, end, b.anchor); err != nil {
		return nil, false, err
	}
	if b.ready {
		a, err := cloneAuth(b.auth)
		return a, true, err
	}
	stop := end
	if end-b.next >= s.Config.BatchSize {
		stop = b.next + s.Config.BatchSize - 1
	}
	a, err := cloneAuth(b.auth)
	if err != nil {
		return nil, false, err
	}
	logs, err := s.authLogs(ctx, a, b.next, stop)
	if err != nil {
		return nil, false, err
	}
	for _, l := range logs {
		if err := a.apply(l, s.Config.AddressManager); err != nil {
			return nil, false, err
		}
	}
	if err := s.check(ctx, end, b.anchor); err != nil {
		return nil, false, err
	}
	b.auth = a
	s.backfills = nil
	b.ready = stop == end
	b.next = stop + 1
	if !b.ready {
		return nil, false, nil
	}
	a, err = cloneAuth(b.auth)
	return a, true, err
}

func (s *Syncer) prepare(ctx context.Context) (store.BlobState, uint64, error) {
	st, err := s.Store.BlobState()
	if err != nil {
		return st, 0, fmt.Errorf("%w: %w", store.ErrIntegrity, err)
	}
	if err := s.checkpoint(ctx, st); err != nil {
		return st, 0, err
	}
	tip, err := s.RPC.HeaderByNumber(ctx, nil)
	if err != nil {
		return st, 0, err
	}
	if tip == nil || tip.Number == nil || !tip.Number.IsUint64() {
		return st, 0, fatal("invalid L1 tip")
	}
	target := uint64(0)
	if tip.Number.Uint64() >= s.Config.Confirmations {
		target = tip.Number.Uint64() - s.Config.Confirmations
	}
	confirmed, err := s.header(ctx, target)
	if err != nil {
		return st, 0, err
	}
	caught := st.Height != nil && *st.Height >= target
	s.Status.SetBlobReady(caught)
	cutoff := uint64(0)
	if confirmed.Time >= store.BlobRetentionSeconds {
		cutoff = confirmed.Time - store.BlobRetentionSeconds
	}
	// Retention progresses even when a later Beacon request fails. It never
	// advances the scan checkpoint and is protected by the same L1 anchors.
	if cutoff > st.Cutoff {
		if err := s.check(ctx, target, confirmed.Hash()); err != nil {
			return st, 0, err
		}
		if err := s.checkpoint(ctx, st); err != nil {
			return st, 0, err
		}
		after := st
		after.Cutoff = cutoff
		after.RetentionHeight = &target
		after.RetentionHash = confirmed.Hash()
		if err := s.commit(ctx, st, after, nil, nil); err != nil {
			return st, 0, err
		}
		st, err = s.Store.BlobState()
		if err != nil {
			return st, 0, fmt.Errorf("%w: %w", store.ErrIntegrity, err)
		}
	}
	return st, target, nil
}
