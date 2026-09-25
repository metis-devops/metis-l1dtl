package blob

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/metis-devops/metis-l1dtl/internal/store"
	"github.com/molecule-man/go-brrr"
)

// Wire order follows data-transport-layer/src/da/blob/channel.ts, including
// Metis origin bits, per-block timestamps, and sequencer signatures.
const maxElements = 10_000_000

type spanReader struct {
	*bytes.Reader
	err error
}

func (r *spanReader) fail(message string) {
	if r.err == nil {
		r.err = fmt.Errorf("%w: %s", ErrInvalid, message)
	}
}
func (r *spanReader) data(n uint64) []byte {
	if r.err != nil {
		return nil
	}
	if n > uint64(r.Len()) {
		r.fail("truncated span")
		return nil
	}
	b := make([]byte, int(n))
	_, r.err = io.ReadFull(r.Reader, b)
	return b
}
func (r *spanReader) u64() uint64 {
	if r.err != nil {
		return 0
	}
	v, err := binary.ReadUvarint(r.Reader)
	if err != nil {
		r.fail("invalid span varint")
	}
	return v
}
func (r *spanReader) bits(n uint64) *big.Int {
	v := new(big.Int).SetBytes(r.data((n + 7) / 8))
	if uint64(v.BitLen()) > n {
		r.fail("nonzero unused bits")
	}
	return v
}
func (r *spanReader) numbers(n uint64) []uint64 {
	out := make([]uint64, int(n))
	for i := range out {
		out[i] = r.u64()
	}
	return out
}
func (r *spanReader) rawTx() []byte {
	if r.err != nil {
		return nil
	}
	prefix, err := r.ReadByte()
	if err != nil {
		r.fail("missing transaction payload")
		return nil
	}
	txType := byte(0)
	if prefix <= 0x7f {
		txType = prefix
		if txType != 1 && txType != 2 {
			r.fail("unsupported transaction type")
			return nil
		}
	} else {
		_ = r.UnreadByte()
	}
	s := rlp.NewStream(r.Reader, uint64(r.Len()))
	payload, err := s.Raw()
	if err != nil {
		r.fail("invalid transaction RLP")
		return nil
	}
	if txType != 0 {
		return append([]byte{txType}, payload...)
	}
	return payload
}

type txFields struct {
	creation, parity, protected, queue, seqParity *big.Int
	sigR, sigS, seqR, seqS                        []*big.Int
	tos                                           []common.Address
	payloads                                      [][]byte
	nonces, gases                                 []uint64
	origins                                       []common.Address
}

func readTransactions(r *spanReader, n uint64) txFields {
	f := txFields{creation: r.bits(n), parity: r.bits(n)}
	for range n {
		f.sigR = append(f.sigR, new(big.Int).SetBytes(r.data(32)))
		f.sigS = append(f.sigS, new(big.Int).SetBytes(r.data(32)))
	}
	for i := range int(n) {
		if f.creation.Bit(i) == 0 {
			f.tos = append(f.tos, common.BytesToAddress(r.data(20)))
		}
	}
	legacy := uint64(0)
	for range n {
		p := r.rawTx()
		f.payloads = append(f.payloads, p)
		if len(p) > 0 && p[0] > 0x7f {
			legacy++
		}
	}
	f.nonces = r.numbers(n)
	f.gases = r.numbers(n)
	f.protected = r.bits(legacy)
	f.queue = r.bits(n)
	f.seqParity = r.bits(n)
	for range n {
		f.seqR = append(f.seqR, new(big.Int).SetBytes(r.data(32)))
		f.seqS = append(f.seqS, new(big.Int).SetBytes(r.data(32)))
	}
	for i := range int(n) {
		if f.queue.Bit(i) == 1 {
			f.origins = append(f.origins, common.BytesToAddress(r.data(20)))
		}
	}
	return f
}

func decodeChannel(data []byte, batchIndex, chainID uint64) ([]store.Block, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: empty compressed channel", ErrInvalid)
	}
	var reader io.Reader
	if data[0] == 1 {
		br := brrr.NewReader(bytes.NewReader(data[1:]))
		defer br.Close() //nolint:errcheck
		reader = br
	} else {
		z, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("%w: invalid zlib channel", ErrInvalid)
		}
		defer z.Close() //nolint:errcheck
		reader = z
	}
	payload, err := io.ReadAll(io.LimitReader(reader, MaxChannelBytes+1))
	if err != nil || len(payload) > MaxChannelBytes {
		return nil, fmt.Errorf("%w: invalid or oversized compressed channel", ErrInvalid)
	}
	stream := rlp.NewStream(bytes.NewReader(payload), uint64(len(payload)))
	var blocks []store.Block
	for {
		raw, err := stream.Bytes()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: channel RLP", ErrInvalid)
		}
		if len(raw) == 0 || raw[0] != 1 {
			return nil, fmt.Errorf("%w: unsupported batch type", ErrInvalid)
		}
		decoded, err := decodeSpan(raw[1:], batchIndex, chainID)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, decoded...)
	}
	return blocks, nil
}
func decodeSpan(data []byte, batchIndex, chainID uint64) ([]store.Block, error) {
	r := &spanReader{Reader: bytes.NewReader(data)}
	start := r.u64()
	r.data(40)
	count := r.u64()
	if r.err != nil {
		return nil, r.err
	}
	if start == 0 || count == 0 || count > maxElements || count > uint64(r.Len())/4 || count-1 > ^uint64(0)-start {
		return nil, fmt.Errorf("%w: span block count/start", ErrInvalid)
	}
	r.bits(count)
	origins := r.numbers(count)
	times := r.numbers(count)
	counts := r.numbers(count)
	total := uint64(0)
	for _, n := range counts {
		if n > maxElements-total {
			return nil, fmt.Errorf("%w: span transaction count", ErrInvalid)
		}
		total += n
	}
	extras := make([][]byte, int(count))
	for i := range extras {
		extras[i] = r.data(r.u64())
	}
	if r.err != nil {
		return nil, r.err
	}
	if total > uint64(r.Len())/128 {
		return nil, fmt.Errorf("%w: truncated transaction signatures", ErrInvalid)
	}
	fields := readTransactions(r, total)
	if r.err != nil {
		return nil, r.err
	}
	if r.Len() != 0 {
		return nil, fmt.Errorf("%w: trailing span bytes", ErrInvalid)
	}
	return deriveBlocks(start, batchIndex, chainID, origins, times, counts, extras, fields)
}

func deriveBlocks(start, batchIndex, chainID uint64, origins, times, counts []uint64, extras [][]byte, fields txFields) ([]store.Block, error) {
	blocks := make([]store.Block, len(counts))
	txIndex, toIndex, originIndex, legacyIndex := 0, 0, 0, 0
	for i := range blocks {
		b := store.Block{Index: start + uint64(i) - 1, BatchIndex: batchIndex, Timestamp: times[i], ExtraData: hexutil.Encode(extras[i]), Confirmed: true, Transactions: make([]store.BlockTransaction, 0, int(counts[i]))}
		for range counts[i] {
			var to *common.Address
			if fields.creation.Bit(txIndex) == 0 {
				a := fields.tos[toIndex]
				to = &a
				toIndex++
			}
			p := fields.payloads[txIndex]
			protected := true
			if p[0] > 0x7f {
				protected = fields.protected.Bit(legacyIndex) == 1
				legacyIndex++
			}
			tx, err := restoreTransaction(p, fields.nonces[txIndex], fields.gases[txIndex], to, chainID, fields.sigR[txIndex], fields.sigS[txIndex], fields.parity.Bit(txIndex), protected)
			if err != nil {
				return nil, fmt.Errorf("%w: transaction reconstruction: %w", ErrInvalid, err)
			}
			entry := store.BlockTransaction{Index: b.Index, BatchIndex: batchIndex, BlockNumber: origins[i], Timestamp: b.Timestamp, GasLimit: strconv.FormatUint(tx.Gas(), 10), Confirmed: true}
			if fields.queue.Bit(txIndex) == 1 {
				n := tx.Nonce()
				entry.QueueOrigin = "l1"
				entry.QueueIndex = &n
				entry.Origin = fields.origins[originIndex]
				originIndex++
				entry.Data = hexutil.Encode(tx.Data())
				entry.Value = "0x0"
			} else {
				if _, err := types.Sender(types.LatestSignerForChainID(new(big.Int).SetUint64(chainID)), tx); err != nil {
					return nil, fmt.Errorf("%w: invalid transaction signature", ErrInvalid)
				}
				raw, err := tx.MarshalBinary()
				if err != nil {
					return nil, err
				}
				entry.QueueOrigin = "sequencer"
				entry.Data = hexutil.Encode(raw)
				entry.Value = hexutil.EncodeBig(tx.Value())
				v := uint64(fields.parity.Bit(txIndex))
				if tx.Type() == 0 && !protected {
					v += 27
				}
				entry.Decoded = &store.DecodedTransaction{Nonce: strconv.FormatUint(tx.Nonce(), 10), GasPrice: tx.GasPrice().String(), GasLimit: entry.GasLimit, Value: entry.Value, Target: tx.To(), Data: hexutil.Encode(tx.Data()), Sig: store.Signature{V: v, R: hexutil.Encode(fields.sigR[txIndex].FillBytes(make([]byte, 32))), S: hexutil.Encode(fields.sigS[txIndex].FillBytes(make([]byte, 32)))}}
				sign := fmt.Sprintf("0x%x,0x%x,0x%x", fields.seqR[txIndex], fields.seqS[txIndex], fields.seqParity.Bit(txIndex))
				entry.SeqSign = &sign
			}
			b.Transactions = append(b.Transactions, entry)
			txIndex++
		}
		// The TS decoder leaves empty-block timestamps at zero.
		if len(b.Transactions) == 0 {
			b.Timestamp = 0
		}
		blocks[i] = b
	}
	return blocks, nil
}

func restoreTransaction(payload []byte, nonce, gas uint64, to *common.Address, chainID uint64, r, s *big.Int, parity uint, protected bool) (*types.Transaction, error) {
	id := new(big.Int).SetUint64(chainID)
	v := new(big.Int).SetUint64(uint64(parity))
	if payload[0] > 0x7f {
		var p struct {
			Value, GasPrice *big.Int
			Data            []byte
		}
		if err := rlp.DecodeBytes(payload, &p); err != nil {
			return nil, err
		}
		if protected {
			v.Add(v, new(big.Int).Add(new(big.Int).Lsh(id, 1), big.NewInt(35)))
		} else {
			v.Add(v, big.NewInt(27))
		}
		return types.NewTx(&types.LegacyTx{Nonce: nonce, Gas: gas, To: to, Value: p.Value, GasPrice: p.GasPrice, Data: p.Data, V: v, R: r, S: s}), nil
	}
	switch payload[0] {
	case 1:
		var p struct {
			Value, GasPrice *big.Int
			Data            []byte
			AccessList      types.AccessList
		}
		if err := rlp.DecodeBytes(payload[1:], &p); err != nil {
			return nil, err
		}
		return types.NewTx(&types.AccessListTx{ChainID: id, Nonce: nonce, Gas: gas, To: to, Value: p.Value, GasPrice: p.GasPrice, Data: p.Data, AccessList: p.AccessList, V: v, R: r, S: s}), nil
	case 2:
		var p struct {
			Value, GasTipCap, GasFeeCap *big.Int
			Data                        []byte
			AccessList                  types.AccessList
		}
		if err := rlp.DecodeBytes(payload[1:], &p); err != nil {
			return nil, err
		}
		return types.NewTx(&types.DynamicFeeTx{ChainID: id, Nonce: nonce, Gas: gas, To: to, Value: p.Value, GasTipCap: p.GasTipCap, GasFeeCap: p.GasFeeCap, Data: p.Data, AccessList: p.AccessList, V: v, R: r, S: s}), nil
	default:
		return nil, fmt.Errorf("unsupported transaction type")
	}
}
