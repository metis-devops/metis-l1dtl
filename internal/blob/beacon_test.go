package blob

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
)

func TestHeaderSlotAndLazyFallback(t *testing.T) {
	calls := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		switch r.URL.Path {
		case "/eth/v1/beacon/genesis":
			_, _ = fmt.Fprint(w, `{"data":{"genesis_time":"100"}}`)
		case "/eth/v1/config/spec":
			_, _ = fmt.Fprint(w, `{"data":{"SECONDS_PER_SLOT":"12"}}`)
		default:
			t.Error("unexpected request", r.URL.Path)
		}
	}))
	defer server.Close()
	b := NewBeacon(server.URL)
	for _, slot := range []uint64{0, 99} {
		got, err := b.Slot(context.Background(), &types.Header{SlotNumber: &slot, Time: 1})
		if err != nil || got != slot {
			t.Fatal(got, err)
		}
	}
	if len(calls) != 0 {
		t.Fatal("header slot made metadata calls")
	}
	for range 2 {
		got, err := b.Slot(context.Background(), &types.Header{Time: 124})
		if err != nil || got != 2 {
			t.Fatal(got, err)
		}
	}
	if calls["/eth/v1/beacon/genesis"] != 1 || calls["/eth/v1/config/spec"] != 1 {
		t.Fatal(calls)
	}
	if _, err := b.Slot(context.Background(), &types.Header{Time: 99}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	b = NewBeacon("http://127.0.0.1:1")
	slot := uint64(88)
	if got, err := b.Slot(context.Background(), &types.Header{SlotNumber: &slot}); err != nil || got != slot {
		t.Fatal(got, err)
	}
}
func TestBeaconMissingRetryAndValidation(t *testing.T) {
	raw, _, _, _ := fixture(t)
	hash := blobHash(t, raw)
	cases := []struct {
		name             string
		status           int
		body             string
		missing, invalid bool
	}{
		{"success", 200, `{"data":["` + hexutil.Encode(raw) + `"]}`, false, false},
		{"pruned", 404, `{"code":404,"message":"Block not found"}`, true, false},
		{"lighthouse404", 404, `{"code":404,"message":"NOT_FOUND: no blobs stored for block 0x123","stacktraces":[]}`, true, false},
		{"empty404", 404, ``, true, false},
		{"auth", 401, `unauthorized`, false, true},
		{"empty", 200, `{"data":[]}`, true, false},
		{"proxy404", 404, `<html>not found</html>`, true, false},
		{"route404", 404, `{"code":404,"message":"route not found"}`, true, false},
		{"retry", 503, `unavailable`, false, false},
		{"rate", 429, `rate limited`, false, false},
		{"invalidJSON", 200, `{`, false, true},
		{"null", 200, `{"data":null}`, false, true},
		{"badBlob", 200, `{"data":["0x01"]}`, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/base/eth/v1/beacon/blobs/42" || r.URL.Query().Get("token") != "test" || r.URL.Query().Get("versioned_hashes") != hash.Hex() {
					t.Error(r.URL)
				}
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer srv.Close()
			b := NewBeacon(srv.URL + "/base?token=test")
			slot := uint64(42)
			frames, err := b.Frames(context.Background(), &types.Header{SlotNumber: &slot}, []common.Hash{hash})
			if tc.name == "success" {
				if err != nil || len(frames) == 0 {
					t.Fatal(err)
				}
				return
			}
			if err == nil || errors.Is(err, ErrMissing) != tc.missing || errors.Is(err, ErrInvalid) != tc.invalid {
				t.Fatal(err)
			}
		})
	}
}
func TestBeaconHashMismatchAndPartial(t *testing.T) {
	raw, _, _, _ := fixture(t)
	hash := blobHash(t, raw)
	other := common.HexToHash("0x01")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []string{hexutil.Encode(raw)}})
	}))
	defer srv.Close()
	b := NewBeacon(srv.URL)
	slot := uint64(1)
	h := &types.Header{SlotNumber: &slot}
	if _, err := b.Frames(context.Background(), h, []common.Hash{other}); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := b.Frames(context.Background(), h, []common.Hash{hash, other}); !errors.Is(err, ErrMissing) {
		t.Fatal(err)
	}
}

func TestMalformedHeaderSlotIsNotMissing(t *testing.T) {
	// The RPC decoder must reject malformed slot values before slot selection.
	raw, err := json.Marshal(&types.Header{Number: big.NewInt(1), Difficulty: big.NewInt(0)})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{`"wrong"`, `"0x10000000000000000"`, `42`} {
		fields["slotNumber"] = json.RawMessage(value)
		encoded, _ := json.Marshal(fields)
		var h types.Header
		if err := json.Unmarshal(encoded, &h); err == nil {
			t.Fatal("invalid slot accepted", value)
		}
	}
}

func TestBeaconReordersMultipleBlobsByVersionedHash(t *testing.T) {
	first, _, _, _ := fixture(t)
	second := append([]byte{}, first...)
	// Change only the first channel-ID byte, preserving valid frame encoding.
	second[6] ^= 1
	firstHash, secondHash := blobHash(t, first), blobHash(t, second)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []string{hexutil.Encode(second), hexutil.Encode(first)}})
	}))
	defer srv.Close()
	slot := uint64(1)
	frames, err := NewBeacon(srv.URL).Frames(context.Background(), &types.Header{SlotNumber: &slot}, []common.Hash{firstHash, secondHash})
	if err != nil || len(frames) != 2 || frames[0].ID[0] != 9 || frames[1].ID[0] != 8 {
		t.Fatal("incorrect Blob ordering", err)
	}
}

func TestBeaconMetadata404IsNotPruning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := NewBeacon(srv.URL).Slot(context.Background(), &types.Header{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing genesis is a configuration error", err)
	}
}
