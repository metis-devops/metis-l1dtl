package integration

import (
	"context"
	"math/big"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MetisProtocol/mvm/l2geth/rollup"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/metis-devops/metis-l1dtl/internal/config"
	"github.com/metis-devops/metis-l1dtl/internal/ingest"
	"github.com/metis-devops/metis-l1dtl/internal/server"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

type rpcStub struct{}

func (rpcStub) HeaderByNumber(_ context.Context, n *big.Int) (*types.Header, error) {
	if n == nil {
		n = big.NewInt(100)
	}
	return &types.Header{Number: n, Time: 123}, nil
}

func TestRealRollupClient(t *testing.T) {
	db, e := store.Open(filepath.Join(t.TempDir(), "db"), store.Identity{Version: 1})
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	status := &ingest.Status{}
	api := &server.Server{Config: config.Config{L2ChainID: 1088, Confirmations: 35}, RPC: rpcStub{}, Store: db, Status: status}
	httpServer := httptest.NewServer(api.Handler())
	defer httpServer.Close()
	client := rollup.NewClient(httpServer.URL, big.NewInt(1088))
	st, e := client.SyncStatus(rollup.BackendL1)
	if e != nil || !st.Syncing {
		t.Fatalf("%+v %v", st, e)
	}
	notFound := func(e error) {
		t.Helper()
		if e == nil || !strings.Contains(e.Error(), "element not found") {
			t.Fatalf("expected client errElementNotFound, got %v", e)
		}
	}
	_, e = client.GetLatestEnqueue()
	notFound(e)
	_, e = client.GetLatestBlockIndex(rollup.BackendL1)
	notFound(e)
	_, e = client.GetLatestTransactionIndex(rollup.BackendL1)
	notFound(e)
	_, e = client.GetLastConfirmedEnqueue()
	notFound(e)
	ctx, e := client.GetEthContext(1)
	if e != nil || ctx.BlockNumber != 1 || ctx.Timestamp != 123 {
		t.Fatalf("%+v %v", ctx, e)
	}
	latest, e := client.GetLatestEthContext()
	if e != nil || latest.BlockNumber != 65 || latest.Timestamp == 0 {
		t.Fatalf("%+v %v", latest, e)
	}
	height := uint64(65)
	record := store.Record{Enqueue: store.Enqueue{Index: 0, GasLimit: "18446744073709551615", Origin: common.HexToAddress("0x123"), Target: common.HexToAddress("0x456"), Data: "0x0102", BlockNumber: 1, Timestamp: 123}}
	if e = db.Commit(store.State{}, store.State{Height: &height}, []store.Record{record}); e != nil {
		t.Fatal(e)
	}
	status.Set(true, nil)
	st, e = client.SyncStatus(rollup.BackendL1)
	if e != nil || st.Syncing {
		t.Fatalf("%+v %v", st, e)
	}
	tx, e := client.GetEnqueue(0)
	if e != nil {
		t.Fatal(e)
	}
	if tx.Gas() != ^uint64(0) || tx.Nonce() != 0 || tx.GetMeta().Index != nil || *tx.GetMeta().QueueIndex != 0 || tx.L1Timestamp() != 123 || tx.To().Hex() != record.Target.Hex() {
		t.Fatalf("bad transaction: %+v", tx)
	}
	idx, e := client.GetLatestEnqueueIndex()
	if e != nil || idx == nil || *idx != 0 {
		t.Fatalf("%v %v", idx, e)
	}
	_, e = client.GetLastConfirmedEnqueue()
	notFound(e)
	h, e := client.GetHighestSynced()
	if e != nil || h != 65 {
		t.Fatalf("%d %v", h, e)
	}
}
