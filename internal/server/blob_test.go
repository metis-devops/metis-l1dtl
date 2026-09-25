package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/metis-devops/metis-l1dtl/internal/blob"
	"github.com/metis-devops/metis-l1dtl/internal/config"
	"github.com/metis-devops/metis-l1dtl/internal/ingest"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

func TestHydrateBlockDepositFromEnqueue(t *testing.T) {
	raw, err := os.ReadFile("../blob/testdata/typescript-blob.hex")
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := hexutil.Decode(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := blob.DecodeBlob(bytes)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := blob.ParseFrames(payload)
	if err != nil {
		t.Fatal(err)
	}
	source := store.BlobTransaction{Hash: common.HexToHash("0x11"), Timestamp: 100, Frames: frames}
	channels, err := blob.DecodeChannels([]store.BlobTransaction{source}, store.BlockBatch{Index: 7, PrevTotalElements: 999, Size: 2}, 1088)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "db"), store.Identity{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	target := common.HexToAddress("0x1234")
	records := []store.Record{{Index: 0, GasLimit: "50000", Data: "0x"}, {Index: 1, GasLimit: "60000", Data: "0xbeef", Target: target, Origin: common.HexToAddress("0x5678"), BlockNumber: 55, Timestamp: 123}}
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
	status.Set(true, nil)
	api := &Server{Config: config.Config{L2ChainID: 1088, Beacon: "enabled"}, Store: db, Status: status}

	for _, path := range []string{"/block/latest/1088", "/block/index/1000/1088"} {
		w := httptest.NewRecorder()
		api.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		var out store.BlockResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || out.Block != nil || out.Batch != nil {
			t.Fatal("missing enqueue", w.Code, w.Body.String())
		}
	}
	if err := db.Commit(store.State{}, store.State{}, records); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/block/latest/1088", "/block/index/1000/1088"} {
		w := httptest.NewRecorder()
		api.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var out store.BlockResponse
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			t.Fatal(err)
		}
		tx := out.Block.Transactions[0]
		if tx.Target != target || tx.Data != "0xbeef" || tx.GasLimit != "60000" {
			t.Fatalf("block API leaked decode placeholders instead of the committed enqueue: target=%s data=%s gas=%s", tx.Target, tx.Data, tx.GasLimit)
		}
	}
}
