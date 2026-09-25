package server

import (
	"context"
	"encoding/json"
	"errors"
	"math/big"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/metis-devops/metis-l1dtl/internal/config"
	"github.com/metis-devops/metis-l1dtl/internal/ingest"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

type rpcStub struct{}

func (rpcStub) HeaderByNumber(_ context.Context, n *big.Int) (*types.Header, error) {
	if n == nil {
		n = big.NewInt(100)
	}
	return &types.Header{Number: n, Time: 123}, nil
}

func TestStorageReadFailureWithdrawsAllDataRoutes(t *testing.T) {
	for _, failingPath := range []string{"/enqueue/latest/1088", "/highest/l1"} {
		t.Run(failingPath, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "db"), store.Identity{Version: 1})
			if err != nil {
				t.Fatal(err)
			}
			status := &ingest.Status{}
			status.Set(true, nil)
			s := &Server{Config: config.Config{L2ChainID: 1088}, RPC: rpcStub{}, Store: db, Status: status}
			h := s.Handler()
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}
			for _, path := range []string{failingPath, "/readyz", "/eth/context/latest", "/eth/syncing/1088", "/enqueue/index/0/1088"} {
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
				if w.Code != 503 {
					t.Fatalf("%s: %d", path, w.Code)
				}
			}
			select {
			case <-status.Done():
			default:
				t.Fatal("storage read failure did not notify ingestion")
			}
		})
	}
}

func TestRoutes(t *testing.T) {
	db, e := store.Open(filepath.Join(t.TempDir(), "db"), store.Identity{Version: 1})
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close() //nolint:errcheck
	status := &ingest.Status{}
	s := &Server{Config: config.Config{L2ChainID: 1088, Confirmations: 35}, RPC: rpcStub{}, Store: db, Status: status, Now: func() time.Time { return time.Unix(456, 0) }}
	h := s.Handler()
	request := func(path string, code int) map[string]any {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != code {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var out map[string]any
		if code != 404 {
			if e := json.Unmarshal(w.Body.Bytes(), &out); e != nil {
				t.Fatal(e)
			}
		}
		return out
	}
	dataRoutes := []string{
		"/enqueue/latest/1088",
		"/enqueue/index/0/1088",
		"/eth/context/latest",
		"/eth/context/blocknumber/65",
		"/highest/l1",
		"/eth/syncing/1088",
		"/transaction/latest/1088",
		"/transaction/index/0/1088",
		"/block/latest/1088",
		"/block/index/0/1088",
	}
	for _, path := range dataRoutes {
		request(path+"?backend=l1", 200)
		for _, query := range []string{"?backend=", "?backend=l2", "?backend=l1&backend=l1"} {
			if out := request(path+query, 400); out["error"] != "only backend=l1 is supported" {
				t.Fatalf("%s%s: %v", path, query, out)
			}
		}
		if before, ok := strings.CutSuffix(path, "/1088"); ok {
			invalidChain := before + "/1?backend=l2"
			if out := request(invalidChain, 400); out["error"] != "unknown or invalid chain ID" {
				t.Fatalf("%s: %v", invalidChain, out)
			}
		}
	}
	for _, path := range []string{"/enqueue/index/bad/1088", "/transaction/index/bad/1088", "/block/index/bad/1088"} {
		if out := request(path, 400); out["error"] != "invalid index" {
			t.Fatalf("%s: %v", path, out)
		}
	}
	for _, p := range []string{"/enqueue/latest/1088", "/enqueue/index/0/1088"} {
		out := request(p, 200)
		if len(out) != 8 || out["index"] != nil {
			t.Fatal(out)
		}
	}
	if request("/eth/syncing/1088", 200)["syncing"] != true {
		t.Fatal("premature readiness")
	}
	request("/readyz", 503)
	for _, p := range []string{"/enqueue/index/-1/1088", "/enqueue/index/18446744073709551616/1088", "/enqueue/latest/1", "/block/latest/1088?backend=l2", "/eth/context/blocknumber/nope", "/transaction/latest/1088?backend="} {
		request(p, 400)
	}
	request("/stateroot/latest/1088", 404)
	if out := request("/eth/context/latest", 200); out["timestamp"] != float64(456) || out["blockNumber"] != float64(65) {
		t.Fatal(out)
	}
	if out := request("/eth/context/blocknumber/65", 200); out["timestamp"] != float64(123) {
		t.Fatal(out)
	}
	if out := request("/eth/context/blocknumber/66", 200); out["blockNumber"] != nil {
		t.Fatal(out)
	}
	for _, p := range []string{"/transaction/latest/1088", "/transaction/index/0/1088", "/block/latest/1088", "/block/index/0/1088"} {
		out := request(p, 200)
		if len(out) != 2 || out["batch"] != nil {
			t.Fatal(out)
		}
	}
	n := uint64(5)
	if e := db.Commit(store.State{}, store.State{Height: &n}, []store.Record{{Index: 0, GasLimit: "18446744073709551615", Data: "0x"}}); e != nil {
		t.Fatal(e)
	}
	if out := request("/enqueue/index/0/1088", 200); out["gasLimit"] != "18446744073709551615" || out["ctcIndex"] != nil {
		t.Fatal(out)
	}
	if out := request("/highest/l1", 200); out["blockNumber"] != float64(5) {
		t.Fatal(out)
	}
	status.Set(true, nil)
	request("/readyz", 200)
	if request("/eth/syncing/1088", 200)["syncing"] != false {
		t.Fatal("not ready")
	}
	status.Set(false, errors.New("reorg"))
	for _, path := range dataRoutes {
		for _, query := range []string{"", "?backend=l2"} {
			if out := request(path+query, 503); out["error"] != "reorg" {
				t.Fatalf("%s%s: %v", path, query, out)
			}
		}
	}
	request("/readyz", 503)
	request("/healthz", 200)
}

func TestBlobStorageFailureAndDisabledCompatibility(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"), store.Identity{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.InitBlob(store.BlobIdentity{Version: 1}); err != nil {
		t.Fatal(err)
	}
	status := &ingest.Status{}
	status.Set(true, nil)
	api := &Server{Config: config.Config{L2ChainID: 1088, Beacon: "enabled"}, Store: db, Status: status}
	handler := api.Handler()
	empty := httptest.NewRecorder()
	handler.ServeHTTP(empty, httptest.NewRequest("GET", "/block/latest/1088", nil))
	if empty.Code != 200 || strings.TrimSpace(empty.Body.String()) != `{"block":null,"batch":null}` {
		t.Fatal(empty.Body.String())
	}
	_ = db.Close()
	for _, path := range []string{"/block/latest/1088", "/enqueue/latest/1088", "/block/index/1/1088", "/readyz"} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 503 {
			t.Fatal(path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
}
