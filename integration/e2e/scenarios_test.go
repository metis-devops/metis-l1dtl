//go:build e2e

package e2e

import (
	"fmt"
	"math/big"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/MetisProtocol/mvm/l2geth/rollup"
)

func TestHistoryAndRollupClient(t *testing.T) {
	for _, historical := range []bool{false, true} {
		t.Run(fmt.Sprintf("registration_before_start=%t", historical), func(t *testing.T) {
			a := newChain(t, historical)
			first := a.enqueue(a.ctc, 1088, []byte{0, 1, 255})
			a.enqueue(a.ctc, 599, []byte{99})
			a.mine(4)
			last := a.enqueue(a.ctc, 1088, []byte{2, 3})
			s := startService(t, a, filepath.Join(t.TempDir(), "db"), a.start, 0)
			s.synced(a.tip())
			s.deposit(a, first, 0, []byte{0, 1, 255})
			want := s.deposit(a, last, 1, []byte{2, 3})
			s.object("/enqueue/latest/1088", want)
			s.object("/enqueue/latest/1088?backend=l1", want)
			s.missing(2)
			s.status("/enqueue/latest/599", 400)
			s.status("/enqueue/latest/1088?backend=l2", 400)
			s.object("/transaction/latest/1088", map[string]any{"transaction": nil, "batch": nil})
			s.object("/block/latest/1088", map[string]any{"block": nil, "batch": nil})
			s.object("/eth/syncing/1088", map[string]any{"syncing": false, "currentTransactionIndex": 0})
			client := rollup.NewClient(s.url, big.NewInt(1088))
			status, err := client.SyncStatus(rollup.BackendL1)
			if err != nil || status.Syncing {
				t.Fatalf("sync status: %+v %v", status, err)
			}
			height, err := client.GetHighestSynced()
			if err != nil || height != a.tip() {
				t.Fatalf("highest: %d %v", height, err)
			}
			index, err := client.GetLatestEnqueueIndex()
			if err != nil || index == nil || *index != 1 {
				t.Fatalf("latest index: %v %v", index, err)
			}
			_, err = client.GetEnqueue(2)
			notFound(t, err)
			_, err = client.GetLastConfirmedEnqueue()
			notFound(t, err)
			_, err = client.GetLatestTransactionIndex(rollup.BackendL1)
			notFound(t, err)
			_, err = client.GetLatestBlockIndex(rollup.BackendL1)
			notFound(t, err)
			block := first.BlockNumber.Uint64()
			header := a.header(block)
			s.object(fmt.Sprintf("/eth/context/blocknumber/%d", block), map[string]any{"blockNumber": block, "blockHash": header.Hash(), "timestamp": header.Time})
			context, err := client.GetEthContext(block)
			if err != nil || context.BlockNumber != block || context.Timestamp != header.Time {
				t.Fatalf("numbered context: %+v %v", context, err)
			}
			before := uint64(time.Now().Unix())
			latest, err := client.GetLatestEthContext()
			if err != nil || latest.BlockNumber != a.tip() || latest.Timestamp < before || latest.Timestamp > uint64(time.Now().Unix()) {
				t.Fatalf("latest context: %+v %v", latest, err)
			}
		})
	}
}

func TestInclusiveDeploymentStart(t *testing.T) {
	a := newChain(t, false)
	a.automine(false)
	next, deployment := a.queueDeployment("CTCFixture")
	change := a.transact(a.managerContract, "setAddress", "CanonicalTransactionChain", next.address)
	deposit := a.enqueueAt(next, 0, []byte{42})
	receipts := a.sameBlock(deployment, change, deposit)
	if receipts[0].ContractAddress != next.address {
		t.Fatal("unexpected CTC deployment address")
	}
	a.start = receipts[0].BlockNumber.Uint64()
	s := startService(t, a, filepath.Join(t.TempDir(), "db"), a.start, 0)
	s.synced(a.tip())
	s.deposit(a, receipts[2], 0, []byte{42})
}

func TestConfirmationBoundary(t *testing.T) {
	a := newChain(t, false)
	a.mine(2)
	s := startService(t, a, filepath.Join(t.TempDir(), "db"), a.start, 2)
	s.synced(a.tip() - 2)
	s.missing(0)
	client := rollup.NewClient(s.url, big.NewInt(1088))
	_, err := client.GetLatestEnqueue()
	notFound(t, err)
	receipt := a.enqueue(a.ctc, 1088, []byte{1})
	s.synced(a.tip() - 2)
	s.missing(0)
	path := fmt.Sprintf("/eth/context/blocknumber/%d", receipt.BlockNumber.Uint64())
	s.object(path, map[string]any{"blockNumber": nil, "blockHash": nil, "timestamp": nil})
	a.mine(1)
	s.synced(a.tip() - 2)
	s.missing(0)
	a.mine(1)
	s.synced(receipt.BlockNumber.Uint64())
	want := s.deposit(a, receipt, 0, []byte{1})
	a.mine(3)
	s.synced(a.tip() - 2)
	s.object("/enqueue/latest/1088", want)
}

func TestSameBlockCTCSwitch(t *testing.T) {
	a := newChain(t, false)
	next, _ := a.deploy("CTCFixture")
	first := a.enqueue(a.ctc, 1088, []byte{0})
	db := filepath.Join(t.TempDir(), "db")
	s := startService(t, a, db, a.start, 0)
	s.synced(a.tip())
	a.automine(false)
	old := a.enqueueAt(a.ctc, 1, []byte{1})
	change := a.transact(a.managerContract, "setAddress", "CanonicalTransactionChain", next.address)
	active := a.enqueueAt(next, 2, []byte{2})
	ignored := a.enqueueAt(a.ctc, 3, []byte{99})
	receipts := a.sameBlock(old, change, active, ignored)
	s.synced(a.tip())
	s.deposit(a, first, 0, []byte{0})
	s.deposit(a, receipts[0], 1, []byte{1})
	s.deposit(a, receipts[2], 2, []byte{2})
	s.missing(3)
	last := a.enqueue(next, 1088, []byte{3})
	s.synced(a.tip())
	s.deposit(a, last, 3, []byte{3})
	s.stop()
	if state := inspectDatabase(t, a, db).State; state.CTC != next.address {
		t.Fatalf("active CTC not persisted: %+v", state)
	}
}

func TestRestartResumes(t *testing.T) {
	a := newChain(t, true)
	first := a.enqueue(a.ctc, 1088, []byte{0})
	db := filepath.Join(t.TempDir(), "db")
	s := startService(t, a, db, a.start, 0)
	s.synced(a.tip())
	want := s.deposit(a, first, 0, []byte{0})
	s.stop()
	before := inspectDatabase(t, a, db)
	last := a.enqueue(a.ctc, 1088, []byte{1})
	a.mine(4)
	s = startService(t, a, db, a.start, 0)
	s.synced(a.tip())
	s.object("/enqueue/index/0/1088", want)
	s.deposit(a, last, 1, []byte{1})
	s.missing(2)
	s.stop()
	after := inspectDatabase(t, a, db)
	if !reflect.DeepEqual(before.Records[0], after.Records[0]) || after.State.Height == nil || *after.State.Height != a.tip() || before.State.Height == nil || *after.State.Height <= *before.State.Height || after.State.Latest == nil || *after.State.Latest != 1 || after.State.CTC != before.State.CTC {
		t.Fatalf("restart state: before=%+v after=%+v", before, after)
	}
}

func TestInvalidWindowIsAtomic(t *testing.T) {
	for _, scenario := range []struct {
		name   string
		index  uint64
		reason string
	}{
		{"gap", 3, "expected queue 2, got 3"},
		{"conflict", 0, "conflicting enqueue 0"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			a := newChain(t, false)
			next, _ := a.deploy("CTCFixture")
			a.enqueue(a.ctc, 1088, []byte{0})
			db := filepath.Join(t.TempDir(), "db")
			s := startService(t, a, db, a.start, 0)
			s.synced(a.tip())
			s.stop()
			before := inspectDatabase(t, a, db)
			s = startService(t, a, db, a.start, 0)
			s.synced(a.tip())
			a.automine(false)
			valid := a.enqueueAt(a.ctc, 1, []byte{1})
			change := a.transact(a.managerContract, "setAddress", "CanonicalTransactionChain", next.address)
			invalid := a.enqueueAt(next, scenario.index, []byte{99})
			a.sameBlock(valid, change, invalid)
			s.halted(scenario.reason)
			s.stop()
			after := inspectDatabase(t, a, db)
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("failed window published state or records: before=%+v after=%+v", before, after)
			}
		})
	}
}

func TestCommittedReorgHalts(t *testing.T) {
	a := newChain(t, false)
	a.enqueue(a.ctc, 1088, []byte{0})
	var snapshot string
	a.call(&snapshot, "evm_snapshot")
	db := filepath.Join(t.TempDir(), "db")
	s := startService(t, a, db, a.start, 0)
	s.synced(a.tip())
	a.enqueue(a.ctc, 1088, []byte{1})
	a.mine(1)
	s.synced(a.tip())
	s.stop()
	before := inspectDatabase(t, a, db)
	s = startService(t, a, db, a.start, 0)
	s.synced(a.tip())
	var reverted bool
	a.call(&reverted, "evm_revert", snapshot)
	if !reverted {
		t.Fatal("Anvil snapshot restore failed")
	}
	a.call(nil, "evm_setNextBlockTimestamp", a.header(a.tip()).Time+100)
	a.enqueue(a.ctc, 1088, []byte{99})
	a.mine(1)
	if a.header(*before.State.Height).Hash() == before.State.Hash {
		t.Fatal("replacement checkpoint must differ")
	}
	s.halted(fmt.Sprintf("committed block %d changed", *before.State.Height))
	s.stop()
	if after := inspectDatabase(t, a, db); !reflect.DeepEqual(before, after) {
		t.Fatal("reorg changed persisted data")
	}
	s = startService(t, a, db, a.start, 0)
	s.halted(fmt.Sprintf("committed block %d changed", *before.State.Height))
	s.stop()
	if after := inspectDatabase(t, a, db); !reflect.DeepEqual(before, after) {
		t.Fatal("restart on reorged checkpoint changed persisted data")
	}
}
