package blob

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

const MaxChannelBytes = 100_000_000

// DecodeBlob reverses the version-zero 254-bit field-element packing used by
// the TypeScript DTL. All unused bytes and the high two field bits must be zero.
func DecodeBlob(raw []byte) ([]byte, error) {
	if len(raw) != 131072 || raw[1] != 0 {
		return nil, fmt.Errorf("%w: Blob size/version", ErrInvalid)
	}
	length := int(raw[2])<<16 | int(raw[3])<<8 | int(raw[4])
	if length > 130044 {
		return nil, fmt.Errorf("%w: Blob payload length", ErrInvalid)
	}
	out := make([]byte, 130044)
	copy(out, raw[5:32])
	op, ip := 28, 32
	encoded := [4]byte{raw[0]}
	if encoded[0]&0xc0 != 0 {
		return nil, fmt.Errorf("%w: field element", ErrInvalid)
	}
	for round := range 1024 {
		start := 0
		if round == 0 {
			start = 1
		}
		for j := start; j < 4; j++ {
			encoded[j] = raw[ip]
			if encoded[j]&0xc0 != 0 {
				return nil, fmt.Errorf("%w: field element", ErrInvalid)
			}
			copy(out[op:], raw[ip+1:ip+32])
			op += 32
			ip += 32
		}
		op--
		out[op-96] = (encoded[0] & 63) | ((encoded[1] & 48) << 2)
		out[op-64] = (encoded[1] & 15) | ((encoded[3] & 15) << 4)
		out[op-32] = (encoded[2] & 63) | ((encoded[3] & 48) << 2)
		if op >= length {
			break
		}
	}
	for _, v := range out[length:] {
		if v != 0 {
			return nil, fmt.Errorf("%w: Blob output padding", ErrInvalid)
		}
	}
	for _, v := range raw[ip:] {
		if v != 0 {
			return nil, fmt.Errorf("%w: Blob padding", ErrInvalid)
		}
	}
	return out[:length], nil
}
func ParseFrames(data []byte) ([]store.Frame, error) {
	if len(data) == 0 || data[0] != 0 {
		return nil, fmt.Errorf("%w: derivation version", ErrInvalid)
	}
	data = data[1:]
	var out []store.Frame
	for len(data) > 0 {
		if len(data) < 23 {
			return nil, fmt.Errorf("%w: truncated frame", ErrInvalid)
		}
		var f store.Frame
		copy(f.ID[:], data[:16])
		f.Number = binary.BigEndian.Uint16(data[16:18])
		n := uint64(binary.BigEndian.Uint32(data[18:22]))
		if n > 1_000_000 || n+23 > uint64(len(data)) {
			return nil, fmt.Errorf("%w: frame length", ErrInvalid)
		}
		if data[22+n] > 1 {
			return nil, fmt.Errorf("%w: frame end flag", ErrInvalid)
		}
		f.Last = data[22+n] == 1
		f.Data = bytes.Clone(data[22 : 22+n])
		out = append(out, f)
		data = data[23+n:]
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: no frames", ErrInvalid)
	}
	return out, nil
}

type frameSource struct {
	frame     store.Frame
	hash      common.Hash
	timestamp uint64
}
type channelInput struct {
	frames map[uint16]frameSource
	end    *uint16
	size   int
}

// DecodeChannels never combines frames from different Inbox submissions.
func DecodeChannels(txs []store.BlobTransaction, batch store.BlockBatch, chainID uint64) ([]store.BlobChannel, error) {
	channels := map[[16]byte]*channelInput{}
	for _, tx := range txs {
		if tx.Missing {
			continue
		}
		for _, f := range tx.Frames {
			c := channels[f.ID]
			if c == nil {
				c = &channelInput{frames: map[uint16]frameSource{}}
				channels[f.ID] = c
			}
			if err := c.add(f, tx); err != nil {
				return nil, err
			}
		}
	}
	ids := make([][16]byte, 0, len(channels))
	for id := range channels {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return bytes.Compare(ids[i][:], ids[j][:]) < 0 })
	var result []store.BlobChannel
	for _, id := range ids {
		ch, err := decodeReadyChannel(channels[id], id, batch, chainID)
		if err != nil {
			return nil, err
		}
		if ch != nil {
			result = append(result, *ch)
		}
	}
	return result, nil
}

func decodeReadyChannel(c *channelInput, id [16]byte, batch store.BlockBatch, chainID uint64) (*store.BlobChannel, error) {
	if c.end == nil || len(c.frames) != int(*c.end)+1 {
		return nil, nil
	}
	data := make([]byte, 0, c.size)
	sources := []common.Hash{}
	seen := map[common.Hash]bool{}
	oldest := ^uint64(0)
	for n := 0; n <= int(*c.end); n++ {
		f, ok := c.frames[uint16(n)]
		if !ok {
			return nil, fmt.Errorf("%w: channel gap", ErrInvalid)
		}
		data = append(data, f.frame.Data...)
		if !seen[f.hash] {
			seen[f.hash] = true
			sources = append(sources, f.hash)
		}
		if f.timestamp < oldest {
			oldest = f.timestamp
		}
	}
	blocks, err := decodeChannel(data, batch.Index, chainID)
	if err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return nil, fmt.Errorf("%w: empty channel", ErrInvalid)
	}
	indexes := make([]uint64, len(blocks))
	for i, b := range blocks {
		if b.Index < batch.PrevTotalElements || b.Index-batch.PrevTotalElements >= batch.Size {
			return nil, fmt.Errorf("%w: block outside Inbox commitment", ErrInvalid)
		}
		indexes[i] = b.Index
	}
	return &store.BlobChannel{Record: store.ChannelRecord{ID: batch.L1TransactionHash.Hex() + "/" + hex.EncodeToString(id[:]), Sources: sources, Blocks: indexes, Batch: batch, ExpiresAt: oldest}, Blocks: blocks}, nil
}

func (c *channelInput) add(f store.Frame, tx store.BlobTransaction) error {
	if _, exists := c.frames[f.Number]; exists {
		return fmt.Errorf("%w: duplicate channel frame", ErrInvalid)
	}
	if f.Last && c.end != nil {
		return fmt.Errorf("%w: duplicate closing frame", ErrInvalid)
	}
	if c.end != nil && f.Number >= *c.end {
		return fmt.Errorf("%w: frame beyond channel end", ErrInvalid)
	}
	if f.Last {
		n := f.Number
		c.end = &n
		for number, old := range c.frames {
			if number > n {
				c.size -= len(old.frame.Data)
				delete(c.frames, number)
			}
		}
	}
	c.size += len(f.Data)
	if c.size > MaxChannelBytes {
		return fmt.Errorf("%w: channel too large", ErrInvalid)
	}
	c.frames[f.Number] = frameSource{f, tx.Hash, tx.Timestamp}
	return nil
}
