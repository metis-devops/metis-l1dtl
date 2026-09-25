package integration

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"math/big"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MetisProtocol/mvm/l2geth/rollup"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/metis-devops/metis-l1dtl/internal/blob"
	"github.com/metis-devops/metis-l1dtl/internal/config"
	"github.com/metis-devops/metis-l1dtl/internal/ingest"
	"github.com/metis-devops/metis-l1dtl/internal/server"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

func TestRealRollupClientBlobBlocks(t *testing.T) {
	raw, err := os.ReadFile("../internal/blob/testdata/typescript-blob.hex")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := hexutil.Decode(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := blob.DecodeBlob(encoded)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := blob.ParseFrames(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("../internal/blob/testdata/typescript-blocks.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected struct {
		Batch store.BlockBatch `json:"transactionBatchEntry"`
	}
	if err := json.Unmarshal(raw, &expected); err != nil {
		t.Fatal(err)
	}
	source := store.BlobTransaction{Hash: common.HexToHash("0x123"), Timestamp: 100, Frames: frames}
	channels, err := blob.DecodeChannels([]store.BlobTransaction{source}, expected.Batch, 1088)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "db"), store.Identity{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InitBlob(store.BlobIdentity{Version: 1}); err != nil {
		t.Fatal(err)
	}
	st, err := db.BlobState()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CommitBlob(st, st, []store.BlobTransaction{source}, channels); err != nil {
		t.Fatal(err)
	}
	status := &ingest.Status{}
	status.EnableBlob()
	status.Set(true, nil)
	status.SetBlobReady(true)
	api := &server.Server{Config: config.Config{L2ChainID: 1088, Beacon: "enabled"}, RPC: rpcStub{}, Store: db, Status: status}
	httpServer := httptest.NewServer(api.Handler())
	defer httpServer.Close()
	client := rollup.NewClient(httpServer.URL, big.NewInt(1088))
	if _, err := client.GetLatestBlock(rollup.BackendL1); err == nil || !strings.Contains(err.Error(), "element not found") {
		t.Fatal("missing enqueue must hide incomplete block", err)
	}
	deposit := store.Enqueue{Index: 1, Target: common.HexToAddress("0x1234"), Origin: common.HexToAddress("0x6666666666666666666666666666666666666666"), Data: "0xbeef", GasLimit: "65000", BlockNumber: 55, Timestamp: 123}
	if err := db.Commit(store.State{}, store.State{}, []store.Record{{Enqueue: store.Enqueue{Index: 0}}, {Enqueue: deposit}}); err != nil {
		t.Fatal(err)
	}
	idx, err := client.GetLatestBlockIndex(rollup.BackendL1)
	if err != nil || idx == nil || *idx != 1000 {
		t.Fatal(idx, err)
	}
	latest, err := client.GetLatestBlock(rollup.BackendL1)
	if err != nil || latest.NumberU64() != 1001 {
		t.Fatal(latest, err)
	}
	if len(latest.Transactions()) != 1 || latest.Transactions()[0].GetMeta().QueueIndex == nil || *latest.Transactions()[0].GetMeta().QueueIndex != 1 {
		t.Fatal("bad deposit conversion")
	}
	if latest.Transactions()[0].L1MessageSender().Hex() != "0x6666666666666666666666666666666666666666" {
		t.Fatal("bad deposit origin")
	}
	tx := latest.Transactions()[0]
	if tx.To() == nil || tx.To().Hex() != deposit.Target.Hex() || hexutil.Encode(tx.Data()) != deposit.Data || tx.Gas() != 65000 || tx.L1BlockNumber().Uint64() != deposit.BlockNumber {
		t.Fatal("RollupClient used deposit placeholders", tx)
	}
	seq, err := client.GetBlock(999, rollup.BackendL1)
	if err != nil {
		t.Fatal(err)
	}
	if seq.NumberU64() != 1000 || seq.Time() != 1700000000 || len(seq.Transactions()) != 1 || seq.Transactions()[0].Gas() != 50000 {
		t.Fatal("bad sequencer conversion")
	}
	wire, err := client.GetRawBlock(999, rollup.BackendL1)
	if err != nil || wire.Batch == nil || wire.Batch.Index != 7 {
		t.Fatal(wire, err)
	}
	if _, err := client.GetBlock(998, rollup.BackendL1); err == nil || !strings.Contains(err.Error(), "element not found") {
		t.Fatal("gap behavior", err)
	}
	st, _ = db.BlobState()
	after := st
	after.Cutoff = 101
	if err := db.CommitBlob(st, after, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetLatestBlock(rollup.BackendL1); err == nil || !strings.Contains(err.Error(), "element not found") {
		t.Fatal("expired tip behavior", err)
	}
}

func TestRealRollupClientMainnetBlobWindow(t *testing.T) {
	manifests, err := filepath.Glob("../internal/blob/testdata/mainnet-*/manifest.json")
	if err != nil || len(manifests) == 0 {
		t.Fatal("missing mainnet fixtures", err)
	}
	for _, manifest := range manifests {
		dir := filepath.Dir(manifest)
		t.Run(filepath.Base(dir), func(t *testing.T) { testRealRollupClientMainnetBlobWindow(t, dir) })
	}
}

func testRealRollupClientMainnetBlobWindow(t *testing.T, dir string) {
	read := func(name string) []byte {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(name, ".gz") {
			r, err := gzip.NewReader(bytes.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			raw, err = io.ReadAll(r)
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Close(); err != nil {
				t.Fatal(err)
			}
		}
		return raw
	}
	var manifest struct {
		Sources []struct {
			TransactionFile string `json:"transactionFile"`
			BeaconFile      string `json:"beaconFile"`
		} `json:"sources"`
		Expected struct {
			Last         uint64 `json:"lastL2Block"`
			Blocks       int    `json:"blockCount"`
			Transactions int    `json:"transactionCount"`
			Deposits     int    `json:"depositCount"`
		} `json:"expected"`
	}
	if err := json.Unmarshal(read("manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	files := [][2]string{{"blob-transaction.json", "beacon-blobs.json.gz"}}
	if len(manifest.Sources) > 0 {
		files = nil
		for _, source := range manifest.Sources {
			files = append(files, [2]string{source.TransactionFile, source.BeaconFile})
		}
	}
	var txs []store.BlobTransaction
	for _, pair := range files {
		var source struct {
			Transaction types.Transaction `json:"transaction"`
			Block       types.Header      `json:"block"`
			Receipt     types.Receipt     `json:"receipt"`
		}
		if err := json.Unmarshal(read(pair[0]), &source); err != nil {
			t.Fatal(err)
		}
		var beacon struct {
			Data []hexutil.Bytes `json:"data"`
		}
		if err := json.Unmarshal(read(pair[1]), &beacon); err != nil {
			t.Fatal(err)
		}
		var frames []store.Frame
		for _, raw := range beacon.Data {
			payload, err := blob.DecodeBlob(raw)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := blob.ParseFrames(payload)
			if err != nil {
				t.Fatal(err)
			}
			frames = append(frames, decoded...)
		}
		txs = append(txs, store.BlobTransaction{Hash: source.Transaction.Hash(), BlockHash: source.Block.Hash(), Height: source.Block.Number.Uint64(), Timestamp: source.Block.Time, TxIndex: source.Receipt.TransactionIndex, Frames: frames})
	}
	var expected struct {
		Batch store.BlockBatch `json:"transactionBatchEntry"`
	}
	if err := json.Unmarshal(read("typescript-blocks.json.gz"), &expected); err != nil {
		t.Fatal(err)
	}
	channels, err := blob.DecodeChannels(txs, expected.Batch, 1088)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "db"), store.Identity{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InitBlob(store.BlobIdentity{Version: 1}); err != nil {
		t.Fatal(err)
	}
	st, err := db.BlobState()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.CommitBlob(st, st, txs, channels); err != nil {
		t.Fatal(err)
	}
	status := &ingest.Status{}
	status.EnableBlob()
	status.Set(true, nil)
	status.SetBlobReady(true)
	api := &server.Server{Config: config.Config{L2ChainID: 1088, Beacon: "enabled"}, Store: db, Status: status}
	httpServer := httptest.NewServer(api.Handler())
	defer httpServer.Close()
	client := rollup.NewClient(httpServer.URL, big.NewInt(1088))
	index, err := client.GetLatestBlockIndex(rollup.BackendL1)
	if err != nil || index == nil || *index != manifest.Expected.Last-1 {
		t.Fatal(index, err)
	}
	blocks, transactions, deposits := 0, 0, 0
	for _, channel := range channels {
		for _, expectedBlock := range channel.Blocks {
			needsEnqueue := false
			for _, tx := range expectedBlock.Transactions {
				if tx.QueueOrigin == "l1" {
					needsEnqueue = true
					deposits++
				}
			}
			if needsEnqueue {
				wire, err := client.GetRawBlock(expectedBlock.Index, rollup.BackendL1)
				if err != nil || wire.Block != nil || wire.Batch != nil {
					t.Fatal("uncaptured enqueue must leave block unavailable", wire, err)
				}
				blocks++
				transactions += len(expectedBlock.Transactions)
				continue
			}
			block, err := client.GetBlock(expectedBlock.Index, rollup.BackendL1)
			if err != nil {
				t.Fatalf("block %d: %v", expectedBlock.Index, err)
			}
			if block.NumberU64() != expectedBlock.Index+1 || block.Time() != expectedBlock.Timestamp || len(block.Transactions()) != len(expectedBlock.Transactions) {
				t.Fatal("client block mismatch", expectedBlock.Index)
			}
			transactions += len(block.Transactions())
			blocks++
		}
	}
	if blocks != manifest.Expected.Blocks || transactions != manifest.Expected.Transactions || deposits != manifest.Expected.Deposits {
		t.Fatal(blocks, transactions, deposits)
	}
}
