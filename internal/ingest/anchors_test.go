package ingest

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"reflect"
	"testing"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

func seedCheckpoint(t *testing.T, s *Syncer, f *fakeRPC) store.State {
	t.Helper()
	before, err := s.Store.State()
	if err != nil {
		t.Fatal(err)
	}
	height := uint64(0)
	after := store.State{Height: &height, Hash: f.headers[0].Hash(), CTC: f.initial}
	if err := s.Store.Commit(before, after, nil); err != nil {
		t.Fatal(err)
	}
	return after
}

func assertStateUnchanged(t *testing.T, s *Syncer, before store.State) {
	t.Helper()
	after, err := s.Store.State()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("state changed: before=%+v after=%+v err=%v", before, after, err)
	}
	i := uint64(0)
	r, err := s.Store.Get(&i)
	if err != nil || r != nil {
		t.Fatalf("partial record: %+v %v", r, err)
	}
}

func TestScanHeaderRequestsConstant(t *testing.T) {
	for _, size := range []uint64{1, 20, 2000} {
		for _, deposit := range []bool{false, true} {
			t.Run(fmt.Sprintf("blocks=%d/deposit=%v", size, deposit), func(t *testing.T) {
				s, f := fixture(t)
				seedCheckpoint(t, s, f)
				s.Config.BatchSize, f.tip = size, size
				// Deliberately discontinuous: interior headers are not needed.
				f.headers[size] = &types.Header{Number: number(size), Time: 123}
				if deposit {
					f.logs = []types.Log{enqueue(t, f, f.initial, 1, 0, 1088, 0)}
				}
				reads := map[uint64]int{}
				tips := 0
				s.RPC = hookedRPC{RPC: f, header: func(ctx context.Context, n *big.Int) (*types.Header, error) {
					if n == nil {
						tips++
					} else {
						if n.Uint64() != 0 && n.Uint64() != size {
							t.Fatalf("unexpected header %s", n)
						}
						reads[n.Uint64()]++
					}
					return f.HeaderByNumber(ctx, n)
				}}
				if caught, err := s.Step(context.Background()); err != nil || !caught {
					t.Fatalf("caught=%v err=%v", caught, err)
				}
				if tips != 1 || reads[0] != 2 || reads[size] != 2 || f.reads != 5 {
					t.Fatalf("tips=%d reads=%v total=%d", tips, reads, f.reads)
				}
				st, err := s.Store.State()
				if err != nil || st.Height == nil || *st.Height != size || st.Hash != f.headers[size].Hash() || (st.Latest != nil) != deposit {
					t.Fatalf("state=%+v err=%v", st, err)
				}
			})
		}
	}
}

func TestScanTrustsLogHash(t *testing.T) {
	s, f := fixture(t)
	seedCheckpoint(t, s, f)
	l := enqueue(t, f, f.initial, 1, 0, 1088, 0)
	l.BlockHash = common.Hash{1}
	f.logs = []types.Log{l}
	if _, err := s.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	i := uint64(0)
	r, err := s.Store.Get(&i)
	if err != nil || r == nil || r.BlockNumber != l.BlockNumber || r.Timestamp != 99 {
		t.Fatalf("record=%+v err=%v", r, err)
	}
}

func TestScanRejectsInvalidLogs(t *testing.T) {
	for _, event := range []string{"address", "enqueue"} {
		for _, mode := range []string{"before", "after", "removed", "topics", "abi", "conflicting block number"} {
			t.Run(event+"/"+mode, func(t *testing.T) {
				s, f := fixture(t)
				before := seedCheckpoint(t, s, f)
				l := enqueue(t, f, f.initial, 1, 0, 1088, 0)
				if event == "address" {
					l = addressEvent(t, s, f, 1, 0, f.initial, f.initial)
				}
				original := l
				switch mode {
				case "before":
					l.BlockNumber = 0
				case "after":
					l.BlockNumber = 3
				case "removed":
					l.Removed = true
				case "topics":
					l.Topics = nil
				case "abi":
					l.Data = append(append([]byte{}, l.Data...), 0)
				case "conflicting block number":
					l.BlockNumber = 2
				}
				s.RPC = hookedRPC{RPC: f, logs: func(_ context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
					if (q.Topics[0][0] == ContractABI.Events["AddressSet"].ID) != (event == "address") {
						return nil, nil
					}
					if mode == "conflicting block number" {
						return []types.Log{original, l}, nil
					}
					return []types.Log{l}, nil
				}}
				if _, err := s.Step(context.Background()); !errors.Is(err, ErrFatal) {
					t.Fatalf("wanted fatal: %v", err)
				}
				assertStateUnchanged(t, s, before)
			})
		}
	}
}

func TestScanAnchorsChangeBeforeCommit(t *testing.T) {
	for _, anchor := range []uint64{0, 2} {
		t.Run(fmt.Sprint(anchor), func(t *testing.T) {
			s, f := fixture(t)
			before := seedCheckpoint(t, s, f)
			f.logs = []types.Log{enqueue(t, f, f.initial, 1, 0, 1088, 0)}
			s.RPC = hookedRPC{RPC: f, logs: func(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
				logs, err := f.FilterLogs(ctx, q)
				if q.Topics[0][0] == ContractABI.Events["TransactionEnqueued"].ID {
					// Mutate the original pointer to verify that anchor hashes are copied.
					f.headers[anchor].Extra = []byte("fork")
				}
				return logs, err
			}}
			if _, err := s.Step(context.Background()); !errors.Is(err, ErrFatal) {
				t.Fatal(err)
			}
			assertStateUnchanged(t, s, before)
		})
	}
}

func TestScanAnchorFailures(t *testing.T) {
	for _, anchor := range []uint64{0, 2} {
		for _, read := range []int{1, 2} {
			for _, mode := range []string{"timeout", "nil", "number"} {
				t.Run(fmt.Sprintf("block=%d/read=%d/%s", anchor, read, mode), func(t *testing.T) {
					s, f := fixture(t)
					before := seedCheckpoint(t, s, f)
					f.logs = []types.Log{enqueue(t, f, f.initial, 1, 0, 1088, 0)}
					reads, fail := 0, true
					var ranges [][2]uint64
					s.RPC = hookedRPC{RPC: f,
						logs: func(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
							ranges = append(ranges, [2]uint64{q.FromBlock.Uint64(), q.ToBlock.Uint64()})
							return f.FilterLogs(ctx, q)
						},
						header: func(ctx context.Context, n *big.Int) (*types.Header, error) {
							if n != nil && n.Uint64() == anchor {
								reads++
								if fail && reads == read {
									switch mode {
									case "timeout":
										return nil, context.DeadlineExceeded
									case "nil":
										return nil, nil
									case "number":
										return &types.Header{Number: number(99)}, nil
									}
								}
							}
							return f.HeaderByNumber(ctx, n)
						},
					}
					want := ErrFatal
					if mode == "timeout" {
						want = context.DeadlineExceeded
					}
					if _, err := s.Step(context.Background()); !errors.Is(err, want) {
						t.Fatalf("got %v want %v", err, want)
					}
					assertStateUnchanged(t, s, before)
					if mode == "timeout" {
						fail = false
						if _, err := s.Step(context.Background()); err != nil {
							t.Fatal(err)
						}
						for _, r := range ranges {
							if r != [2]uint64{1, 2} {
								t.Fatalf("retry changed range: %v", ranges)
							}
						}
						st, _ := s.Store.State()
						if st.Latest == nil || *st.Latest != 0 || st.Height == nil || *st.Height != 2 {
							t.Fatalf("retry did not commit: %+v", st)
						}
					}
				})
			}
		}
	}
}

func TestBootstrapHeaderRequestsConstant(t *testing.T) {
	for _, events := range []uint64{1, 100} {
		t.Run(fmt.Sprint(events), func(t *testing.T) {
			s, f := fixture(t)
			s.Config.Start, s.Config.BatchSize = 101, 101
			f.noInitialEvent = true
			f.headers[100] = &types.Header{Number: number(100)}
			old := common.Address{}
			for i := range events {
				f.headers[i] = &types.Header{Number: number(i)}
				l := addressEvent(t, s, f, i, 0, old, f.initial)
				l.BlockHash = common.BigToHash(number(i + 1)) // Trust unbound event hashes.
				f.logs = append(f.logs, l)
				old = f.initial
			}
			s.RPC = hookedRPC{RPC: f, header: func(ctx context.Context, n *big.Int) (*types.Header, error) {
				if n == nil || n.Uint64() != 100 {
					t.Fatalf("unexpected event header: %v", n)
				}
				return f.HeaderByNumber(ctx, n)
			}}
			w := scanWindow{from: s.Config.Start}
			if ready, err := s.initializeCTC(context.Background(), &w); err != nil || !ready || w.ctc != f.initial {
				t.Fatalf("ready=%v ctc=%s err=%v", ready, w.ctc, err)
			}
			if f.reads != 3 {
				t.Fatalf("header requests=%d", f.reads)
			}
			assertUncommitted(t, s)
		})
	}
}

func TestBootstrapPageAnchorChangeDoesNotPublish(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			s, f := fixture(t)
			s.Config.Start, s.Config.BatchSize = 3, 2
			f.noInitialEvent = true
			if !empty {
				f.logs = []types.Log{addressEvent(t, s, f, 1, 0, common.Address{}, f.initial)}
			}
			s.RPC = hookedRPC{RPC: f, logs: func(ctx context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
				logs, err := f.FilterLogs(ctx, q)
				f.headers[2].Extra = []byte("fork")
				return logs, err
			}}
			w := scanWindow{from: 3}
			if ready, err := s.initializeCTC(context.Background(), &w); ready || !errors.Is(err, ErrFatal) {
				t.Fatalf("ready=%v err=%v", ready, err)
			}
			if s.bootstrap.ready || s.bootstrap.next != 2 || s.bootstrap.ctc != (common.Address{}) || w.ctc != (common.Address{}) {
				t.Fatalf("published failed page: %+v", s.bootstrap)
			}
			assertUncommitted(t, s)
		})
	}
}

func TestGenesisScanSkipsBootstrap(t *testing.T) {
	s, f := fixture(t)
	s.Config.Start = 0
	s.Config.BatchSize = 4
	f.logs = []types.Log{enqueue(t, f, f.initial, 1, 0, 1088, 0)}
	s.RPC = hookedRPC{RPC: f, header: func(ctx context.Context, n *big.Int) (*types.Header, error) {
		if n != nil && (!n.IsUint64() || n.Uint64() != 3) {
			t.Fatalf("unexpected header: %v", n)
		}
		return f.HeaderByNumber(ctx, n)
	}}
	if caught, err := s.Step(context.Background()); err != nil || !caught {
		t.Fatalf("caught=%v err=%v", caught, err)
	}
	if f.reads != 3 || s.bootstrap != nil {
		t.Fatalf("reads=%d bootstrap=%+v", f.reads, s.bootstrap)
	}
}
