// Package blob implements the Metis DTL Blob transport and wire decoder.
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto/kzg4844"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

var ErrInvalid = errors.New("invalid Blob evidence")
var ErrMissing = errors.New("blob data unavailable")

// Beacon is used by one ingestion goroutine. Slot conversion metadata is lazy:
// headers containing SlotNumber never depend on genesis/spec availability.
type Beacon struct {
	URL              string
	Client           *http.Client
	genesis, seconds uint64
	clockReady       bool
}

func NewBeacon(endpoint string) *Beacon {
	return &Beacon{URL: endpoint, Client: &http.Client{Timeout: 30 * time.Second}}
}
func (b *Beacon) request(ctx context.Context, path string, query url.Values, dst any) error {
	u, err := url.Parse(b.URL)
	if err != nil {
		return fmt.Errorf("%w: invalid Beacon URL", ErrInvalid)
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	q := u.Query()
	maps.Copy(q, query)
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return fmt.Errorf("Beacon request construction failed")
	}
	req.Header.Set("Accept", "application/json")
	resp, err := b.Client.Do(req)
	if err != nil {
		return fmt.Errorf("Beacon request failed (%s)", path)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode == http.StatusNotFound && strings.HasPrefix(path, "/eth/v1/beacon/blobs/") {
		return ErrMissing
	}
	// Bound even malformed/error responses, without logging URL credentials or bodies.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20+1))
	if err != nil {
		return fmt.Errorf("Beacon response read failed")
	}
	if len(raw) > 32<<20 {
		return fmt.Errorf("%w: oversized Beacon response", ErrInvalid)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			return fmt.Errorf("Beacon HTTP %d; retry", resp.StatusCode)
		}
		return fmt.Errorf("%w: Beacon HTTP %d (configuration or unsupported API)", ErrInvalid, resp.StatusCode)
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("%w: malformed Beacon JSON", ErrInvalid)
	}
	return nil
}
func (b *Beacon) Slot(ctx context.Context, header *types.Header) (uint64, error) {
	if header == nil {
		return 0, fmt.Errorf("%w: missing Blob header", ErrInvalid)
	}
	if header.SlotNumber != nil {
		return *header.SlotNumber, nil
	}
	if !b.clockReady {
		var genesis struct {
			Data struct {
				Time string `json:"genesis_time"`
			} `json:"data"`
		}
		var spec struct {
			Data struct {
				Seconds string `json:"SECONDS_PER_SLOT"`
			} `json:"data"`
		}
		if err := b.request(ctx, "/eth/v1/beacon/genesis", nil, &genesis); err != nil {
			return 0, err
		}
		if err := b.request(ctx, "/eth/v1/config/spec", nil, &spec); err != nil {
			return 0, err
		}
		g, e1 := strconv.ParseUint(genesis.Data.Time, 10, 64)
		s, e2 := strconv.ParseUint(spec.Data.Seconds, 10, 64)
		if e1 != nil || e2 != nil || s == 0 {
			return 0, fmt.Errorf("%w: invalid Beacon clock", ErrInvalid)
		}
		b.genesis, b.seconds, b.clockReady = g, s, true
	}
	if header.Time < b.genesis {
		return 0, fmt.Errorf("%w: timestamp precedes Beacon genesis", ErrInvalid)
	}
	return (header.Time - b.genesis) / b.seconds, nil
}
func (b *Beacon) Frames(ctx context.Context, h *types.Header, hashes []common.Hash) ([]store.Frame, error) {
	slot, err := b.Slot(ctx, h)
	if err != nil {
		return nil, err
	}
	query := url.Values{}
	wanted := map[common.Hash]bool{}
	for _, hash := range hashes {
		if !wanted[hash] {
			query.Add("versioned_hashes", hash.Hex())
			wanted[hash] = true
		}
	}
	var response struct {
		Data json.RawMessage `json:"data"`
	}
	if err := b.request(ctx, "/eth/v1/beacon/blobs/"+strconv.FormatUint(slot, 10), query, &response); err != nil {
		return nil, err
	}
	if len(response.Data) == 0 || string(response.Data) == "null" {
		return nil, fmt.Errorf("%w: missing blobs array", ErrInvalid)
	}
	var blobs []hexutil.Bytes
	if err := json.Unmarshal(response.Data, &blobs); err != nil {
		return nil, fmt.Errorf("%w: malformed blobs array", ErrInvalid)
	}
	byHash := map[common.Hash][]store.Frame{}
	for _, raw := range blobs {
		if len(raw) != 131072 {
			return nil, fmt.Errorf("%w: wrong Blob length", ErrInvalid)
		}
		data := kzg4844.Blob(raw)
		commitment, err := kzg4844.BlobToCommitment(&data)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid KZG Blob", ErrInvalid)
		}
		hash := common.Hash(kzg4844.CalcBlobHashV1(sha256.New(), &commitment))
		if !wanted[hash] {
			return nil, fmt.Errorf("%w: unexpected Blob hash", ErrInvalid)
		}
		if _, exists := byHash[hash]; exists {
			return nil, fmt.Errorf("%w: duplicate returned Blob", ErrInvalid)
		}
		payload, err := DecodeBlob(raw)
		if err != nil {
			return nil, err
		}
		frames, err := ParseFrames(payload)
		if err != nil {
			return nil, err
		}
		byHash[hash] = frames
	}
	var frames []store.Frame
	for _, hash := range hashes {
		f, ok := byHash[hash]
		if !ok {
			return nil, ErrMissing
		}
		frames = append(frames, f...)
	}
	return frames, nil
}
