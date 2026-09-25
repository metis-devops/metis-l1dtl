package blob

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

type mainnetSource struct {
	TransactionFile string        `json:"transactionFile"`
	BeaconFile      string        `json:"beaconFile"`
	Slot            uint64        `json:"slot"`
	Hash            common.Hash   `json:"transactionHash"`
	Hashes          []common.Hash `json:"blobVersionedHashes"`
}
type mainnetManifest struct {
	Slot     uint64            `json:"slot"`
	Hashes   []common.Hash     `json:"blobVersionedHashes"`
	Sources  []mainnetSource   `json:"sources"`
	SHA256   map[string]string `json:"sha256"`
	Expected struct {
		Batch        uint64 `json:"batchIndex"`
		First        uint64 `json:"firstL2Block"`
		Last         uint64 `json:"lastL2Block"`
		Blocks       int    `json:"blockCount"`
		Transactions int    `json:"transactionCount"`
		Deposits     int    `json:"depositCount"`
	} `json:"expected"`
}
type mainnetRecord struct {
	Transaction types.Transaction `json:"transaction"`
	Receipt     types.Receipt     `json:"receipt"`
	Block       types.Header      `json:"block"`
}

func mainnetDirectories(t *testing.T) []string {
	t.Helper()
	manifests, err := filepath.Glob("testdata/mainnet-*/manifest.json")
	if err != nil || len(manifests) == 0 {
		t.Fatal("missing mainnet fixtures", err)
	}
	dirs := make([]string, len(manifests))
	for i, p := range manifests {
		dirs[i] = filepath.Dir(p)
	}
	return dirs
}
func readMainnetFile(t *testing.T, dir, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	if strings.HasSuffix(name, ".gz") {
		r, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		data, err = io.ReadAll(r)
		if err != nil {
			t.Fatal(err)
		}
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return data
}
func mainnetMetadata(t *testing.T, dir string) mainnetManifest {
	t.Helper()
	var manifest mainnetManifest
	if err := json.Unmarshal(readMainnetFile(t, dir, "manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Sources) == 0 {
		manifest.Sources = []mainnetSource{{TransactionFile: "blob-transaction.json", BeaconFile: "beacon-blobs.json.gz", Slot: manifest.Slot, Hashes: manifest.Hashes}}
	}
	if manifest.Expected.Blocks == 0 {
		t.Fatal("missing pinned oracle summary")
	}
	return manifest
}
func mainnetOracle(t *testing.T, dir string) (store.BlockBatch, []store.Block) {
	t.Helper()
	var oracle map[string]json.RawMessage
	if err := json.Unmarshal(readMainnetFile(t, dir, "typescript-blocks.json.gz"), &oracle); err != nil {
		t.Fatal(err)
	}
	var blocks []map[string]json.RawMessage
	if err := json.Unmarshal(oracle["blockEntries"], &blocks); err != nil {
		t.Fatal(err)
	}
	// Only normalize the upstream deposit-origin prefix defect. Leave the captured
	// TypeScript oracle unchanged and compare every other decoded field directly.
	for _, block := range blocks {
		var txs []map[string]json.RawMessage
		if err := json.Unmarshal(block["transactions"], &txs); err != nil {
			t.Fatal(err)
		}
		for _, tx := range txs {
			var origin string
			if err := json.Unmarshal(tx["origin"], &origin); err != nil {
				t.Fatal(err)
			}
			if len(origin) == 40 {
				tx["origin"], _ = json.Marshal("0x" + origin)
			}
		}
		block["transactions"], _ = json.Marshal(txs)
	}
	oracle["blockEntries"], _ = json.Marshal(blocks)
	normalized, _ := json.Marshal(oracle)
	var expected struct {
		Batch  store.BlockBatch `json:"transactionBatchEntry"`
		Blocks []store.Block    `json:"blockEntries"`
	}
	if err := json.Unmarshal(normalized, &expected); err != nil {
		t.Fatal(err)
	}
	return expected.Batch, expected.Blocks
}
func mainnetSources(t *testing.T, dir string, manifest mainnetManifest) []store.BlobTransaction {
	t.Helper()
	records := make([]mainnetRecord, len(manifest.Sources))
	responses := make([][]byte, len(records))
	needsClock := false
	for i, spec := range manifest.Sources {
		if err := json.Unmarshal(readMainnetFile(t, dir, spec.TransactionFile), &records[i]); err != nil {
			t.Fatal(err)
		}
		src := &records[i]
		hash := src.Transaction.Hash()
		if src.Receipt.TxHash != hash || src.Receipt.BlockHash != src.Block.Hash() || !reflect.DeepEqual(src.Transaction.BlobHashes(), spec.Hashes) {
			t.Fatal("source provenance mismatch")
		}
		if spec.Hash != (common.Hash{}) && hash != spec.Hash {
			t.Fatal("unexpected Blob transaction")
		}
		responses[i] = readMainnetFile(t, dir, spec.BeaconFile)
		needsClock = needsClock || src.Block.SlotNumber == nil
	}
	calls := map[string]int{}
	genesis := readMainnetFile(t, dir, "beacon-genesis.json")
	specification := readMainnetFile(t, dir, "beacon-spec.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.URL.Path]++
		switch r.URL.Path {
		case "/eth/v1/beacon/genesis":
			_, _ = w.Write(genesis)
			return
		case "/eth/v1/config/spec":
			_, _ = w.Write(specification)
			return
		}
		for i, source := range manifest.Sources {
			if r.URL.Path != "/eth/v1/beacon/blobs/"+strconv.FormatUint(source.Slot, 10) {
				continue
			}
			hashes := make([]string, len(source.Hashes))
			for j, h := range source.Hashes {
				hashes[j] = h.Hex()
			}
			if reflect.DeepEqual(r.URL.Query()["versioned_hashes"], hashes) {
				_, _ = w.Write(responses[i])
				return
			}
		}
		t.Error("unexpected Beacon request", r.URL)
		http.NotFound(w, r)
	}))
	defer srv.Close()
	beacon := NewBeacon(srv.URL)
	txs := make([]store.BlobTransaction, len(records))
	for i := range records {
		src := &records[i]
		frames, err := beacon.Frames(context.Background(), &src.Block, src.Transaction.BlobHashes())
		if err != nil {
			t.Fatal(err)
		}
		txs[i] = store.BlobTransaction{Hash: src.Transaction.Hash(), BlockHash: src.Block.Hash(), Height: src.Block.Number.Uint64(), Timestamp: src.Block.Time, TxIndex: src.Receipt.TransactionIndex, Frames: frames}
	}
	clockCalls := 0
	if needsClock {
		clockCalls = 1
	}
	if calls["/eth/v1/beacon/genesis"] != clockCalls || calls["/eth/v1/config/spec"] != clockCalls {
		t.Fatal("unexpected slot resolution", calls)
	}
	return txs
}
func TestMainnetBlobTypeScriptGolden(t *testing.T) {
	for _, dir := range mainnetDirectories(t) {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			manifest := mainnetMetadata(t, dir)
			batch, want := mainnetOracle(t, dir)
			sources := mainnetSources(t, dir, manifest)
			channels, err := DecodeChannels(sources, batch, 1088)
			if err != nil {
				t.Fatal(err)
			}
			var got []store.Block
			for _, ch := range channels {
				got = append(got, ch.Blocks...)
			}
			sort.Slice(got, func(i, j int) bool { return got[i].Index < got[j].Index })
			sort.Slice(want, func(i, j int) bool { return want[i].Index < want[j].Index })
			if len(got) != len(want) || len(got) != manifest.Expected.Blocks {
				t.Fatalf("block counts: Go=%d TS=%d pinned=%d", len(got), len(want), manifest.Expected.Blocks)
			}
			if batch.Index != manifest.Expected.Batch || got[0].Index+1 != manifest.Expected.First || got[len(got)-1].Index+1 != manifest.Expected.Last {
				t.Fatal("unexpected batch/range")
			}
			for i := range got {
				if !reflect.DeepEqual(got[i], want[i]) {
					a, _ := json.Marshal(got[i])
					b, _ := json.Marshal(want[i])
					t.Fatalf("block %d mismatch\nGo: %s\nTS: %s", got[i].Index, a, b)
				}
			}
			transactions, deposits := 0, 0
			for _, block := range got {
				for _, tx := range block.Transactions {
					transactions++
					if tx.QueueOrigin == "l1" {
						deposits++
					}
				}
			}
			if transactions != manifest.Expected.Transactions || deposits != manifest.Expected.Deposits {
				t.Fatal("unexpected transaction counts", transactions, deposits)
			}
			t.Logf("mainnet batch=%d blocks=%d transactions=%d deposits=%d sources=%d; KZG and TypeScript output verified", batch.Index, len(got), transactions, deposits, len(sources))
		})
	}
}
func TestMainnetFixtureChecksums(t *testing.T) {
	for _, dir := range mainnetDirectories(t) {
		t.Run(filepath.Base(dir), func(t *testing.T) {
			manifest := mainnetMetadata(t, dir)
			for name, want := range manifest.SHA256 {
				data, err := os.ReadFile(filepath.Join(dir, name))
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(data)
				if hex.EncodeToString(sum[:]) != want {
					t.Fatal("fixture checksum mismatch", name)
				}
			}
		})
	}
}
