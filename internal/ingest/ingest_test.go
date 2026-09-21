package ingest

import (
	"context"
	"errors"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

func TestScanSwitchFilterDuplicateResume(t *testing.T) {
	s, f := fixture(t)
	next := common.HexToAddress("0x300")
	d, e := ContractABI.Events["AddressSet"].Inputs.NonIndexed().Pack(next, f.initial)
	if e != nil {
		t.Fatal(e)
	}
	first := enqueue(t, f, f.initial, 1, 0, 1088, 0)
	f.logs = []types.Log{first, first, enqueue(t, f, f.initial, 1, 17, 999, 1), {Address: s.Config.AddressManager, BlockNumber: 2, BlockHash: f.headers[2].Hash(), Index: 0, Topics: []common.Hash{ContractABI.Events["AddressSet"].ID, nameHash}, Data: d}, enqueue(t, f, next, 2, 1, 1088, 1), enqueue(t, f, f.initial, 2, 88, 1088, 2)}
	caught, e := s.Step(context.Background())
	if e != nil || caught {
		t.Fatalf("%v %v", caught, e)
	}
	st, _ := s.Store.State()
	if *st.Height != 2 || *st.Latest != 1 || st.CTC != next {
		t.Fatalf("%+v", st)
	}
	// A fresh syncer resumes an empty range without re-reading old events.
	resumed := &Syncer{Config: s.Config, RPC: f, Store: s.Store, Status: &Status{}}
	caught, e = resumed.Step(context.Background())
	if e != nil || !caught {
		t.Fatalf("%v %v", caught, e)
	}
	ready, _ := resumed.Status.Snapshot()
	if !ready {
		t.Fatal("not ready")
	}
	r, e := s.Store.Get(nil)
	if e != nil || r.Index != 1 || r.GasLimit != "18446744073709551615" {
		t.Fatalf("%+v %v", r, e)
	}
}
func TestGapAtomicity(t *testing.T) {
	s, f := fixture(t)
	f.logs = []types.Log{enqueue(t, f, f.initial, 1, 0, 1088, 0), enqueue(t, f, f.initial, 2, 2, 1088, 0)}
	_, e := s.Step(context.Background())
	if !errors.Is(e, store.ErrIntegrity) {
		t.Fatal(e)
	}
	st, _ := s.Store.State()
	if st.Height != nil || st.Latest != nil {
		t.Fatal("partial commit")
	}
	n := uint64(0)
	r, _ := s.Store.Get(&n)
	if r != nil {
		t.Fatal("partial records")
	}
}
func TestRPCFailureRetryAndReorg(t *testing.T) {
	s, f := fixture(t)
	f.failure = errors.New("offline")
	if _, e := s.Step(context.Background()); e == nil {
		t.Fatal("wanted error")
	}
	st, _ := s.Store.State()
	if st.Height != nil {
		t.Fatal("advanced")
	}
	f.failure = nil
	if _, e := s.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	f.headers[2].Extra = []byte("fork")
	if _, e := s.Step(context.Background()); !errors.Is(e, ErrFatal) {
		t.Fatal(e)
	}
}
func TestConfirmationBoundary(t *testing.T) {
	s, f := fixture(t)
	s.Config.Confirmations = 2
	f.logs = []types.Log{enqueue(t, f, f.initial, 2, 0, 1088, 0)}
	caught, e := s.Step(context.Background())
	if e != nil || !caught {
		t.Fatalf("%v %v", caught, e)
	}
	st, _ := s.Store.State()
	if *st.Height != 1 || st.Latest != nil {
		t.Fatalf("%+v", st)
	}
}
func TestMalformedEvent(t *testing.T) {
	_, f := fixture(t)
	l := enqueue(t, f, f.initial, 1, 0, 1088, 0)
	l.Data = append(l.Data, 0)
	if _, _, e := Decode(l); !errors.Is(e, ErrFatal) {
		t.Fatal(e)
	}
	l = enqueue(t, f, f.initial, 1, 0, 1088, 0)
	l.Topics[3][0] = 1
	if _, _, e := Decode(l); !errors.Is(e, ErrFatal) {
		t.Fatal(e)
	}
}
func TestFatalRunWithdrawsReadiness(t *testing.T) {
	s, f := fixture(t)
	s.Status.Set(true, nil)
	f.logs = []types.Log{enqueue(t, f, f.initial, 1, 1, 1088, 0)}
	s.Run(context.Background())
	ready, e := s.Status.Snapshot()
	if ready || e == nil {
		t.Fatalf("%v %v", ready, e)
	}
}
func TestConflictingDuplicate(t *testing.T) {
	s, f := fixture(t)
	l := enqueue(t, f, f.initial, 1, 0, 1088, 0)
	other := l
	other.TxHash = common.HexToHash("0xbad")
	f.logs = []types.Log{l, other}
	if _, e := s.Step(context.Background()); !errors.Is(e, ErrFatal) {
		t.Fatal(e)
	}
}
func TestStartAheadOfConfirmedTip(t *testing.T) {
	s, _ := fixture(t)
	s.Config.Start = 4
	if _, e := s.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	ready, _ := s.Status.Snapshot()
	if ready {
		t.Fatal("ready before start scanned")
	}
}

func TestManagerDeploymentAndSameBlockEnqueue(t *testing.T) {
	s, f := fixture(t)
	f.noInitialEvent = true
	raw, e := ContractABI.Events["AddressSet"].Inputs.NonIndexed().Pack(f.initial, common.Address{})
	if e != nil {
		t.Fatal(e)
	}
	f.logs = []types.Log{
		{Address: s.Config.AddressManager, BlockNumber: 1, BlockHash: f.headers[1].Hash(), Index: 0, Topics: []common.Hash{ContractABI.Events["AddressSet"].ID, nameHash}, Data: raw},
		enqueue(t, f, f.initial, 1, 0, 1088, 1),
	}
	if _, e := s.Step(context.Background()); e != nil {
		t.Fatal(e)
	}
	r, e := s.Store.Get(nil)
	if e != nil || r == nil || r.Index != 0 {
		t.Fatalf("%+v %v", r, e)
	}
}
