package ingest

import (
	"context"
	"math/big"
	"path/filepath"
	"testing"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/metis-devops/metis-l1dtl/internal/config"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

type fakeRPC struct {
	noInitialEvent bool
	headers        map[uint64]*types.Header
	tip            uint64
	logs           []types.Log
	initial        common.Address
	failure        error
	reads          int
}

func (f *fakeRPC) HeaderByNumber(_ context.Context, n *big.Int) (*types.Header, error) {
	if f.failure != nil {
		return nil, f.failure
	}
	i := f.tip
	if n != nil {
		i = n.Uint64()
	}
	f.reads++
	h := f.headers[i]
	return h, nil
}

func (f *fakeRPC) FilterLogs(_ context.Context, q ethereum.FilterQuery) ([]types.Log, error) {
	var out []types.Log
	logs := append([]types.Log{}, f.logs...)
	if !f.noInitialEvent {
		data, _ := ContractABI.Events["AddressSet"].Inputs.NonIndexed().Pack(f.initial, common.Address{})
		logs = append(logs, types.Log{Address: common.HexToAddress("0x200"), BlockNumber: 0, BlockHash: f.headers[0].Hash(), Topics: []common.Hash{ContractABI.Events["AddressSet"].ID, nameHash}, Data: data})
	}
	for _, l := range logs {
		if l.BlockNumber < q.FromBlock.Uint64() || l.BlockNumber > q.ToBlock.Uint64() {
			continue
		}
		matched := false
		for _, a := range q.Addresses {
			if a == l.Address {
				matched = true
			}
		}
		if !matched {
			continue
		}
		if len(q.Topics) > 0 && l.Topics[0] != q.Topics[0][0] {
			continue
		}
		out = append(out, l)
	}
	return out, nil
}

func fixture(t *testing.T) (*Syncer, *fakeRPC) {
	t.Helper()
	f := &fakeRPC{headers: map[uint64]*types.Header{}, tip: 3, initial: common.HexToAddress("0x100")}
	var parent common.Hash
	for i := uint64(0); i <= 4; i++ {
		h := &types.Header{Number: new(big.Int).SetUint64(i), ParentHash: parent, Time: 100 + i}
		f.headers[i] = h
		parent = h.Hash()
	}
	db, e := store.Open(filepath.Join(t.TempDir(), "test.db"), store.Identity{Version: 1})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = db.Close() })
	s := &Syncer{Config: config.Config{Start: 1, BatchSize: 2, L2ChainID: 1088, AddressManager: common.HexToAddress("0x200")}, RPC: f, Store: db, Status: &Status{}}
	return s, f
}

func enqueue(t *testing.T, f *fakeRPC, addr common.Address, block, idx, chain uint64, logIndex uint) types.Log {
	t.Helper()
	data, e := ContractABI.Events["TransactionEnqueued"].Inputs.NonIndexed().Pack(new(big.Int).SetUint64(chain), new(big.Int).SetUint64(^uint64(0)), []byte{1, 2}, big.NewInt(99))
	if e != nil {
		t.Fatal(e)
	}
	return types.Log{Address: addr, BlockNumber: block, BlockHash: f.headers[block].Hash(), TxHash: common.HexToHash("0x123"), Index: logIndex, Topics: []common.Hash{ContractABI.Events["TransactionEnqueued"].ID, common.HexToHash("0x111"), common.HexToHash("0x222"), common.BigToHash(new(big.Int).SetUint64(idx))}, Data: data}
}
