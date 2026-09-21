package ingest

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

func number(n uint64) *big.Int { return new(big.Int).SetUint64(n) }

func (s *Syncer) header(ctx context.Context, n uint64) (*types.Header, error) {
	h, e := s.RPC.HeaderByNumber(ctx, number(n))
	if e != nil {
		return nil, e
	}
	if h == nil || h.Number == nil || !h.Number.IsUint64() || h.Number.Uint64() != n {
		return nil, fmt.Errorf("%w: invalid header %d", ErrFatal, n)
	}
	return h, nil
}

func (s *Syncer) Run(ctx context.Context) {
	ctx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	go func() {
		select {
		case <-s.Status.Done():
			cancelRun()
		case <-ctx.Done():
		}
	}()
	for ctx.Err() == nil {
		scanCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		caught, err := s.Step(scanCtx)
		cancel()
		if _, fatal := s.Status.Snapshot(); fatal != nil {
			slog.Error("sync stopped", "error", fatal)
			return
		}
		if err != nil {
			s.Status.Set(false, nil)
			if errors.Is(err, ErrFatal) || errors.Is(err, store.ErrIntegrity) {
				s.Status.Set(false, err)
				slog.Error("sync stopped", "error", err)
				return
			}
			slog.Warn("L1 scan failed; retrying", "error", err)
		}
		if err == nil && !caught {
			continue
		}
		timer := time.NewTimer(s.Config.Poll)
		select {
		case <-s.Status.Done():
			timer.Stop()
			return
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
