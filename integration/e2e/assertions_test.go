//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/MetisProtocol/mvm/l2geth/rollup"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

var httpClient = &http.Client{Timeout: 2 * time.Second}

func (s *service) get(path string) (int, map[string]json.RawMessage, error) {
	response, err := httpClient.Get(s.url + path)
	if err != nil {
		return 0, nil, err
	}
	var body map[string]json.RawMessage
	err = json.NewDecoder(response.Body).Decode(&body)
	if closeErr := response.Body.Close(); err == nil {
		err = closeErr
	}
	return response.StatusCode, body, err
}

func (s *service) status(path string, want int) map[string]json.RawMessage {
	s.t.Helper()
	code, body, err := s.get(path)
	if err != nil || code != want {
		s.t.Fatalf("GET %s: status=%d body=%s error=%v; want %d", path, code, body, err, want)
	}
	return body
}

func (s *service) object(path string, want any) {
	s.t.Helper()
	body := s.status(path, http.StatusOK)
	raw, err := json.Marshal(want)
	if err != nil {
		s.t.Fatal(err)
	}
	var expected map[string]json.RawMessage
	if err := json.Unmarshal(raw, &expected); err != nil {
		s.t.Fatal(err)
	}
	if !reflect.DeepEqual(body, expected) {
		s.t.Fatalf("GET %s: got %s, want %s", path, body, expected)
	}
}

func (s *service) synced(height uint64) {
	s.t.Helper()
	eventually(s.t, fmt.Sprintf("checkpoint %d and readiness", height), func() (bool, error) {
		code, body, err := s.get("/highest/l1")
		if err != nil {
			return false, err
		}
		if code != 200 || string(body["blockNumber"]) != fmt.Sprint(height) {
			return false, fmt.Errorf("checkpoint: status=%d body=%s", code, body)
		}
		code, _, err = s.get("/readyz")
		return code == 200, err
	})
}

func (s *service) missing(index uint64) {
	s.t.Helper()
	s.object(fmt.Sprintf("/enqueue/index/%d/1088", index), map[string]any{
		"index": nil, "target": nil, "data": nil, "gasLimit": nil,
		"origin": nil, "blockNumber": nil, "timestamp": nil, "ctcIndex": nil,
	})
}

func (s *service) deposit(a *chain, receipt *types.Receipt, index uint64, data []byte) store.Enqueue {
	s.t.Helper()
	header := a.header(receipt.BlockNumber.Uint64())
	want := store.Enqueue{Index: index, Target: depositTarget, Origin: a.account, GasLimit: fmt.Sprint(depositGas), Data: hexutil.Encode(data), BlockNumber: receipt.BlockNumber.Uint64(), Timestamp: header.Time}
	s.object(fmt.Sprintf("/enqueue/index/%d/1088", index), want)
	client := rollup.NewClient(s.url, big.NewInt(1088))
	tx, err := client.GetEnqueue(index)
	if err != nil {
		s.t.Fatal(err)
	}
	meta := tx.GetMeta()
	if tx.To() == nil || tx.To().Hex() != depositTarget.Hex() || tx.Gas() != depositGas || tx.Nonce() != index || !bytes.Equal(tx.Data(), data) || tx.L1Timestamp() != header.Time || meta.Index != nil || meta.QueueIndex == nil || *meta.QueueIndex != index || tx.L1BlockNumber() == nil || tx.L1BlockNumber().Uint64() != want.BlockNumber || tx.L1MessageSender() == nil || tx.L1MessageSender().Hex() != a.account.Hex() {
		s.t.Fatalf("bad RollupClient transaction: %+v, meta %+v", tx, meta)
	}
	return want
}

func notFound(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "element not found") {
		t.Fatalf("expected client element not found, got %v", err)
	}
}

func (s *service) halted(reason string) {
	s.t.Helper()
	eventually(s.t, "integrity failure: "+reason, func() (bool, error) {
		return strings.Contains(s.logs.String(), `"msg":"sync stopped"`) && strings.Contains(s.logs.String(), reason), nil
	})
	s.object("/healthz", map[string]bool{"alive": true})
	for _, path := range []string{"/readyz", "/enqueue/latest/1088", "/highest/l1", "/eth/syncing/1088", "/eth/context/latest", "/transaction/latest/1088", "/block/latest/1088"} {
		s.status(path, http.StatusServiceUnavailable)
	}
}

type databaseSnapshot struct {
	State   store.State
	Records []*store.Enqueue
}

func inspectDatabase(t *testing.T, a *chain, path string) databaseSnapshot {
	t.Helper()
	db, err := store.Open(path, store.Identity{Version: 1, L1: 31337, L2: 1088, Start: a.start, Manager: a.manager})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Error(err)
		}
	}()
	state, err := db.State()
	if err != nil {
		t.Fatal(err)
	}
	snapshot := databaseSnapshot{State: state}
	// Probe past the latest index as well to detect accidentally published rows.
	end := uint64(3)
	if state.Latest != nil {
		end += *state.Latest
	}
	for index := uint64(0); index <= end; index++ {
		r, err := db.Get(&index)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.Records = append(snapshot.Records, r)
	}
	return snapshot
}
