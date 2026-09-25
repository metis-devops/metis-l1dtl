package ingest

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

func TestFatalStatusStopsRunBeforeScanning(t *testing.T) {
	s, f := fixture(t)
	s.Status.Set(false, store.ErrIntegrity)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s.Run(ctx)
	if f.reads != 0 {
		t.Fatal("scanned after fatal status")
	}
	assertUncommitted(t, s)
}

func TestFatalStatusCancelsActiveRPC(t *testing.T) {
	s, f := fixture(t)
	entered := make(chan struct{})
	s.RPC = hookedRPC{RPC: f, logs: func(ctx context.Context, _ ethereum.FilterQuery) ([]types.Log, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("RPC did not start")
	}
	s.Status.Set(false, store.ErrIntegrity)
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("fatal status did not cancel RPC")
	}
	assertUncommitted(t, s)
}

func TestFatalStatusPreventsCommitAfterLastRPC(t *testing.T) {
	s, f := fixture(t)
	s.Config.BatchSize = 10
	f.logs = []types.Log{enqueue(t, f, f.initial, 1, 0, 1088, 0)}
	endReads := 0
	s.RPC = hookedRPC{RPC: f, header: func(ctx context.Context, n *big.Int) (*types.Header, error) {
		h, err := f.HeaderByNumber(ctx, n)
		if n != nil && n.Uint64() == f.tip {
			endReads++
			if endReads == 2 {
				s.Status.Set(false, store.ErrIntegrity)
			}
		}
		return h, err
	}}
	if _, err := s.Step(context.Background()); !errors.Is(err, store.ErrIntegrity) {
		t.Fatalf("wanted fatal error, got %v", err)
	}
	assertUncommitted(t, s)
	zero := uint64(0)
	if got, err := s.Store.Get(&zero); got != nil || err != nil {
		t.Fatalf("partial record: %v, %v", got, err)
	}
}

func TestKnownLagWithdrawsReadinessBeforeLogs(t *testing.T) {
	s, f := fixture(t)
	s.Config.BatchSize = 10
	if caught, err := s.Step(context.Background()); err != nil || !caught {
		t.Fatalf("initial scan: %v, %v", caught, err)
	}
	f.tip = 4
	calls := 0
	s.RPC = hookedRPC{RPC: f, logs: func(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
		calls++
		if ready, err := s.Status.Snapshot(); ready || err != nil {
			t.Fatalf("status while behind: %v, %v", ready, err)
		}
		return f.FilterLogs(ctx, q)
	}}
	if caught, err := s.Step(context.Background()); err != nil || !caught {
		t.Fatalf("catch-up scan: %v, %v", caught, err)
	}
	if ready, err := s.Status.Snapshot(); !ready || err != nil || calls == 0 {
		t.Fatalf("status after catch-up: %v, %v, calls=%d", ready, err, calls)
	}
}

func TestFatalStatusIsTerminal(t *testing.T) {
	s := &Status{}
	done := s.Done()
	s.Set(false, store.ErrIntegrity)
	s.Set(true, nil)
	s.Set(true, errors.New("later error"))
	if ready, err := s.Snapshot(); ready || !errors.Is(err, store.ErrIntegrity) {
		t.Fatalf("lost first fatal status: %v, %v", ready, err)
	}
	select {
	case <-done:
	default:
		t.Fatal("halt notification not closed")
	}
	if err := s.commitIfHealthy(func() error {
		t.Fatal("commit ran after halt")
		return nil
	}); !errors.Is(err, store.ErrIntegrity) {
		t.Fatal(err)
	}
}

func TestFatalPublicationWaitsForInFlightCommit(t *testing.T) {
	s := &Status{}
	entered, release := make(chan struct{}), make(chan struct{})
	committed := make(chan error, 1)
	go func() {
		committed <- s.commitIfHealthy(func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	halted := make(chan struct{})
	go func() { s.Set(false, store.ErrIntegrity); close(halted) }()
	select {
	case <-halted:
		t.Fatal("published halt during commit")
	default:
	}
	close(release)
	if err := <-committed; err != nil {
		t.Fatal(err)
	}
	<-halted
	if err := s.commitIfHealthy(func() error {
		t.Fatal("new commit after halt")
		return nil
	}); !errors.Is(err, store.ErrIntegrity) {
		t.Fatal(err)
	}
}

func TestIndependentBlobReadiness(t *testing.T) {
	s := &Status{}
	s.EnableBlob()
	s.Set(true, nil)
	if ready, _ := s.Snapshot(); ready {
		t.Fatal("ready before Blob caught up")
	}
	s.SetBlobReady(true)
	if ready, _ := s.Snapshot(); !ready {
		t.Fatal("not ready")
	}
	s.Set(false, nil)
	if ready, _ := s.Snapshot(); ready {
		t.Fatal("deposit lag hidden")
	}
	s.Set(true, nil)
	s.SetBlobReady(false)
	if err := s.CommitIfHealthy(func() error { return nil }); err != nil {
		t.Fatal("lag blocked deposit commit", err)
	}
	s.Set(false, ErrFatal)
	s.SetBlobReady(true)
	s.Set(true, nil)
	if ready, err := s.Snapshot(); ready || err == nil {
		t.Fatal("fatal cleared")
	}
	if err := s.CommitIfHealthy(func() error { t.Fatal("commit after halt"); return nil }); err == nil {
		t.Fatal("missing fatal")
	}
}
