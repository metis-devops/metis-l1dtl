package blob

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/metis-devops/metis-l1dtl/internal/store"
	"github.com/molecule-man/go-brrr"
)

func fixture(t *testing.T) ([]byte, []store.Frame, store.BlockBatch, []store.Block) {
	t.Helper()
	raw, err := os.ReadFile("testdata/typescript-blob.hex")
	if err != nil {
		t.Fatal(err)
	}
	data, err := hexutil.Decode(strings.TrimSpace(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	payload, err := DecodeBlob(data)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := ParseFrames(payload)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("testdata/typescript-blocks.json")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Batch  store.BlockBatch `json:"transactionBatchEntry"`
		Blocks []store.Block    `json:"blockEntries"`
	}
	// The TS Blob path omits 0x on deposit origins; the real Go client
	// requires an address. Normalize that known formatting defect only.
	raw = bytes.ReplaceAll(raw, []byte(`"origin": "6666666666666666666666666666666666666666"`), []byte(`"origin": "0x6666666666666666666666666666666666666666"`))
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return data, frames, result.Batch, result.Blocks
}
func TestTypeScriptGolden(t *testing.T) {
	_, frames, batch, want := fixture(t)
	for _, compression := range []string{"zlib", "brotli"} {
		t.Run(compression, func(t *testing.T) {
			data := frames[0].Data
			if compression == "brotli" {
				z, err := zlib.NewReader(bytes.NewReader(data))
				if err != nil {
					t.Fatal(err)
				}
				plain, err := io.ReadAll(z)
				if err != nil {
					t.Fatal(err)
				}
				_ = z.Close()
				var out bytes.Buffer
				out.WriteByte(1)
				w, err := brrr.NewWriter(&out, 6)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := w.Write(plain); err != nil {
					t.Fatal(err)
				}
				if err := w.Close(); err != nil {
					t.Fatal(err)
				}
				data = out.Bytes()
			}
			decoded, err := decodeChannel(data, batch.Index, 1088)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(decoded, want) {
				got, _ := json.MarshalIndent(decoded, "", "  ")
				t.Fatalf("golden mismatch: %s", got)
			}
		})
	}
}
func TestChannelSourcesAndMissingFrames(t *testing.T) {
	_, frames, batch, want := fixture(t)
	first, last := frames[0], frames[0]
	split := len(first.Data) / 2
	first.Data = bytes.Clone(first.Data[:split])
	first.Last = false
	last.Data = bytes.Clone(last.Data[split:])
	last.Number = 1
	txs := []store.BlobTransaction{{Hash: common.HexToHash("0x01"), Timestamp: 100, Frames: []store.Frame{first}}, {Hash: common.HexToHash("0x02"), Timestamp: 200, Frames: []store.Frame{last}}}
	out, err := DecodeChannels(txs, batch, 1088)
	if err != nil || len(out) != 1 {
		t.Fatalf("%v %v", out, err)
	}
	if !reflect.DeepEqual(out[0].Blocks, want) || len(out[0].Record.Sources) != 2 || out[0].Record.ExpiresAt != 100 {
		t.Fatal("bad channel provenance")
	}
	txs[0].Missing = true
	out, err = DecodeChannels(txs, batch, 1088)
	if err != nil || len(out) != 0 {
		t.Fatal("published incomplete channel", err)
	}
	txs[0].Missing = false
	txs[1].Frames = []store.Frame{first}
	if _, err = DecodeChannels(txs, batch, 1088); err == nil {
		t.Fatal("accepted duplicate frame")
	}
}
func TestMalformedCodecs(t *testing.T) {
	raw, frames, _, _ := fixture(t)
	for _, mutate := range []func([]byte){func(b []byte) { b[0] |= 0xc0 }, func(b []byte) { b[1] = 1 }, func(b []byte) { b[len(b)-1] = 1 }, func(b []byte) { b[2] = 255 }} {
		b := bytes.Clone(raw)
		mutate(b)
		if _, err := DecodeBlob(b); err == nil {
			t.Fatal("accepted malformed blob")
		}
	}
	for _, p := range [][]byte{nil, {1}, {0}, {0, 1, 2}} {
		if _, err := ParseFrames(p); err == nil {
			t.Fatal("accepted malformed frames")
		}
	}
	for _, p := range [][]byte{nil, {0}, {1, 255, 255}, frames[0].Data[:4]} {
		if _, err := decodeChannel(p, 0, 1088); err == nil {
			t.Fatal("accepted broken compression")
		}
	}
}
func blobHash(t *testing.T, raw []byte) common.Hash {
	t.Helper()
	b := kzg4844.Blob(raw)
	c, err := kzg4844.BlobToCommitment(&b)
	if err != nil {
		t.Fatal(err)
	}
	return common.Hash(kzg4844.CalcBlobHashV1(sha256.New(), &c))
}
func FuzzDecodeSpan(f *testing.F) {
	f.Add([]byte{0})
	f.Add(bytes.Repeat([]byte{255}, 64))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) < 1<<20 {
			_, _ = decodeSpan(data, 0, 1088)
		}
	})
}
