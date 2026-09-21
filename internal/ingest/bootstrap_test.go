package ingest

import (
	"context"
	"errors"
	"math/big"
	"testing"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

type hookedRPC struct {
	RPC
	logs   func(context.Context, ethereum.FilterQuery) ([]types.Log, error)
	header func(context.Context, *big.Int) (*types.Header, error)
}

func (r hookedRPC) FilterLogs(c context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	if r.logs != nil {
		return r.logs(c, q)
	}
	return r.RPC.FilterLogs(c, q)
}
func (r hookedRPC) HeaderByNumber(c context.Context, n *big.Int) (*types.Header, error) {
	if r.header != nil {
		return r.header(c, n)
	}
	return r.RPC.HeaderByNumber(c, n)
}
func addressEvent(t *testing.T, s *Syncer, f *fakeRPC, block uint64, index uint, old, next common.Address) types.Log {
	t.Helper()
	data, err := ContractABI.Events["AddressSet"].Inputs.NonIndexed().Pack(next, old)
	if err != nil {
		t.Fatal(err)
	}
	return types.Log{Address: s.Config.AddressManager, BlockNumber: block, BlockHash: f.headers[block].Hash(), Index: index, Topics: []common.Hash{ContractABI.Events["AddressSet"].ID, nameHash}, Data: data}
}
func assertUncommitted(t *testing.T, s *Syncer) {
	t.Helper()
	st, err := s.Store.State()
	if err != nil || st.Height != nil || st.Latest != nil {
		t.Fatalf("partial commit: %+v %v", st, err)
	}
}

func TestBootstrapPagingRetryAndResume(t *testing.T) {
	s, f := fixture(t)
	s.Config.Start = 3
	s.Config.BatchSize = 1
	// Historical page [2,2] is empty; it must not advance the deposit checkpoint.
	if done, err := s.Step(context.Background()); err != nil || done {
		t.Fatalf("%v %v", done, err)
	}
	assertUncommitted(t, s)
	if ready, _ := s.Status.Snapshot(); ready || s.bootstrap.next != 1 {
		t.Fatal("unexpected progress")
	}
	var ranges [][2]uint64
	fail := true
	s.RPC = hookedRPC{RPC: f, logs: func(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
		ranges = append(ranges, [2]uint64{q.FromBlock.Uint64(), q.ToBlock.Uint64()})
		if fail {
			return nil, context.DeadlineExceeded
		}
		return f.FilterLogs(ctx, q)
	}}
	if _, err := s.Step(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if s.bootstrap.next != 1 {
		t.Fatal("failed page advanced")
	}
	fail = false
	if _, err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ranges[0] != ranges[1] || s.bootstrap.next != 0 {
		t.Fatalf("ranges %v", ranges)
	}
	if done, err := s.Step(context.Background()); err != nil || !done {
		t.Fatalf("%v %v", done, err)
	}
	st, _ := s.Store.State()
	if st.Height == nil || *st.Height != 3 || st.CTC != f.initial {
		t.Fatalf("%+v", st)
	}
	resumed := &Syncer{Config: s.Config, Store: s.Store, Status: &Status{}, RPC: hookedRPC{RPC: f, logs: func(context.Context, ethereum.FilterQuery) ([]types.Log, error) {
		t.Fatal("resume queried old logs")
		return nil, nil
	}}}
	if _, err := resumed.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestBootstrapAddressHistory(t *testing.T) {
	for _, mode := range []string{"latest", "zero", "none", "genesis"} {
		t.Run(mode, func(t *testing.T) {
			s, f := fixture(t)
			f.noInitialEvent = true
			s.Config.Start = 3
			s.Config.BatchSize = 2
			next := common.HexToAddress("0x300")
			f.logs = []types.Log{addressEvent(t, s, f, 1, 0, common.Address{}, f.initial), addressEvent(t, s, f, 2, 1, f.initial, next)}
			f.logs = append(f.logs, f.logs[0]) // Exact duplicate, returned out of order.
			want := next
			switch mode {
			case "zero":
				f.logs[1] = addressEvent(t, s, f, 2, 1, f.initial, common.Address{})
				want = common.Address{}
			case "none":
				f.logs = nil
				want = common.Address{}
			case "genesis":
				s.Config.Start = 0
				f.logs = nil
				want = common.Address{}
			}
			w := scanWindow{from: s.Config.Start}
			for range 4 {
				ready, err := s.initializeCTC(context.Background(), &w)
				if err != nil {
					t.Fatal(err)
				}
				if ready {
					if w.ctc != want {
						t.Fatalf("got %s want %s", w.ctc, want)
					}
					assertUncommitted(t, s)
					return
				}
			}
			t.Fatal("initialization did not finish")
		})
	}
}

func TestBootstrapRejectsInvalidEvidence(t *testing.T) {
	for _, mode := range []string{"range", "address", "topics", "abi", "removed", "conflict", "transition", "missing genesis predecessor"} {
		t.Run(mode, func(t *testing.T) {
			s, f := fixture(t)
			s.Config.Start = 3
			s.Config.BatchSize = 3
			f.noInitialEvent = true
			a := addressEvent(t, s, f, 1, 0, common.Address{}, f.initial)
			logs := []types.Log{a, a} // Exact duplicates are otherwise valid.
			switch mode {
			case "range":
				logs[0].BlockNumber = 3
			case "address":
				logs[0].Address = common.Address{}
			case "topics":
				logs[0].Topics = nil
			case "abi":
				logs[0].Data = append(append([]byte{}, a.Data...), 0)
			case "removed":
				logs[0].Removed = true
			case "conflict":
				logs[1].TxHash = common.HexToHash("0x01")
			case "transition":
				logs = append(logs, addressEvent(t, s, f, 2, 1, common.Address{}, common.HexToAddress("0x300")))
			case "missing genesis predecessor":
				logs = []types.Log{addressEvent(t, s, f, 1, 0, f.initial, common.Address{})}
			}
			s.RPC = hookedRPC{RPC: f, logs: func(context.Context, ethereum.FilterQuery) ([]types.Log, error) { return logs, nil }}
			if _, err := s.Step(context.Background()); !errors.Is(err, ErrFatal) {
				t.Fatalf("wanted integrity failure: %v", err)
			}
			assertUncommitted(t, s)
		})
	}
}

func TestBootstrapAnchorAndTrustedHistory(t *testing.T) {
	for _, mode := range []string{"anchor", "event", "first parent"} {
		t.Run(mode, func(t *testing.T) {
			s, f := fixture(t)
			s.Config.Start = 2
			w := scanWindow{from: 2}
			if ready, err := s.initializeCTC(context.Background(), &w); err != nil || !ready {
				t.Fatalf("%v %v", ready, err)
			}
			switch mode {
			case "anchor":
				f.headers[1].Extra = []byte("fork")
			case "event":
				// Event blocks are trusted when the initialization anchor is stable.
				cp := *f.headers[0]
				cp.Extra = []byte("fork")
				f.headers[0] = &cp
			case "first parent":
				f.headers[2].ParentHash = common.Hash{}
			}
			if _, err := s.Step(context.Background()); mode == "anchor" {
				if !errors.Is(err, ErrFatal) {
					t.Fatal(err)
				}
				assertUncommitted(t, s)
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestBootstrapFirstCommitRechecksAnchor(t *testing.T) {
	s, f := fixture(t)
	s.RPC = hookedRPC{RPC: f, logs: func(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
		logs, err := f.FilterLogs(ctx, q)
		// Discovery and headers are complete; change the anchor during deposit
		// fetching, leaving scan headers unchanged to exercise commit revalidation.
		if q.Topics[0][0] == ContractABI.Events["TransactionEnqueued"].ID {
			cp := *f.headers[0]
			cp.Extra = []byte("fork")
			f.headers[0] = &cp
		}
		return logs, err
	}}
	if _, err := s.Step(context.Background()); !errors.Is(err, ErrFatal) {
		t.Fatal(err)
	}
	assertUncommitted(t, s)
}

func TestBootstrapCancellationPreservesPage(t *testing.T) {
	s, f := fixture(t)
	s.Config.Start = 3
	s.Config.BatchSize = 1
	if _, err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.RPC = hookedRPC{RPC: f, logs: func(ctx context.Context, _ ethereum.FilterQuery) ([]types.Log, error) { return nil, ctx.Err() }}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Step(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if s.bootstrap.next != 1 || s.bootstrap.ready {
		t.Fatal("cancel advanced discovery")
	}
	assertUncommitted(t, s)
}

func TestBootstrapPageValidationFailureRetriesWithoutPublishing(t *testing.T) {
	for _, stage := range []string{"check anchor", "recheck anchor"} {
		t.Run(stage, func(t *testing.T) {
			s, f := fixture(t)
			s.Config.Start = 3
			s.Config.BatchSize = 2
			f.noInitialEvent = true
			f.logs = []types.Log{addressEvent(t, s, f, 1, 0, common.Address{}, f.initial)}
			reads := make(map[uint64]int)
			fail := true
			var ranges [][2]uint64
			s.RPC = hookedRPC{RPC: f,
				logs: func(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
					ranges = append(ranges, [2]uint64{q.FromBlock.Uint64(), q.ToBlock.Uint64()})
					return f.FilterLogs(ctx, q)
				},
				header: func(ctx context.Context, n *big.Int) (*types.Header, error) {
					block := n.Uint64()
					reads[block]++
					if fail && block == 2 && ((stage == "check anchor" && reads[block] == 2) || (stage == "recheck anchor" && reads[block] == 3)) {
						return nil, context.DeadlineExceeded
					}
					return f.HeaderByNumber(ctx, n)
				},
			}
			w := scanWindow{from: s.Config.Start}
			if ready, err := s.initializeCTC(context.Background(), &w); ready || !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("ready=%v err=%v", ready, err)
			}
			b := s.bootstrap
			if b.next != 2 || b.ready || b.ctc != (common.Address{}) || w.ctc != (common.Address{}) {
				t.Fatalf("failed page published progress: %+v, ctc=%s", b, w.ctc)
			}
			assertUncommitted(t, s)
			fail = false
			if ready, err := s.initializeCTC(context.Background(), &w); !ready || err != nil {
				t.Fatalf("retry: ready=%v err=%v", ready, err)
			}
			wantPages := 1
			if stage == "recheck anchor" {
				wantPages = 2
			}
			if len(ranges) != wantPages || ranges[0] != [2]uint64{1, 2} || ranges[len(ranges)-1] != ranges[0] {
				t.Fatalf("retry ranges: %v", ranges)
			}
			if w.ctc != f.initial || !b.ready {
				t.Fatalf("retry did not publish validated page: %+v, ctc=%s", b, w.ctc)
			}
			assertUncommitted(t, s)
		})
	}
}
