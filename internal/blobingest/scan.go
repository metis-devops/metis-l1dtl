package blobingest

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/metis-devops/metis-l1dtl/internal/blob"
	"github.com/metis-devops/metis-l1dtl/internal/store"
)

func (s *Syncer) scan(ctx context.Context, a *authorization, logs []types.Log, from, end, cutoff uint64) ([]store.BlobTransaction, []store.BlobChannel, error) {
	result := &scanResult{cache: map[common.Hash]store.BlobTransaction{}}
	position := 0
	for height := from; ; height++ {
		block, err := s.RPC.BlockByNumber(ctx, number(height))
		if err != nil {
			return nil, nil, err
		}
		if block == nil || !block.Number().IsUint64() || block.NumberU64() != height {
			return nil, nil, fatal("invalid Inbox block")
		}
		for i, tx := range block.Transactions() {
			for position < len(logs) && (logs[position].BlockNumber < height || (logs[position].BlockNumber == height && logs[position].TxIndex < uint(i))) {
				if err := a.apply(logs[position], s.Config.AddressManager); err != nil {
					return nil, nil, err
				}
				position++
			}
			if err := s.submission(ctx, a, tx, block, uint(i), cutoff, result); err != nil {
				return nil, nil, err
			}
		}
		for position < len(logs) && logs[position].BlockNumber == height {
			if err := a.apply(logs[position], s.Config.AddressManager); err != nil {
				return nil, nil, err
			}
			position++
		}
		if height == end {
			break
		}
	}
	if position != len(logs) {
		return nil, nil, fatal("unapplied authorization logs")
	}
	return result.txs, result.channels, nil
}
func (s *Syncer) transactionSender(tx *types.Transaction) (common.Address, error) {
	if tx.ChainId().Cmp(number(s.Config.L1ChainID)) != 0 {
		return common.Address{}, fatal("Inbox transaction L1 chain mismatch")
	}
	sender, err := types.Sender(types.LatestSignerForChainID(number(s.Config.L1ChainID)), tx)
	if err != nil {
		return common.Address{}, fatal("invalid Inbox transaction signature")
	}
	return sender, nil
}
func (s *Syncer) receipt(ctx context.Context, hash common.Hash, b *types.Block, index uint) (*types.Receipt, error) {
	r, err := s.RPC.TransactionReceipt(ctx, hash)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, fmt.Errorf("L1 receipt unavailable; retry")
	}
	if r.TxHash != hash || r.BlockHash != b.Hash() || r.BlockNumber == nil || r.BlockNumber.Cmp(b.Number()) != 0 || r.TransactionIndex != index {
		return nil, fatal("receipt provenance mismatch")
	}
	return r, nil
}
func parseSubmission(tx *types.Transaction, b *types.Block, sender common.Address) (store.BlockBatch, []common.Hash, error) {
	var batch store.BlockBatch
	data := tx.Data()
	if len(data) < 102 || (len(data)-70)%32 != 0 || data[1] != 0 {
		return batch, nil, fatal("invalid Blob Inbox commitment")
	}
	index := new(big.Int).SetBytes(data[2:34])
	start := new(big.Int).SetBytes(data[34:66])
	size := uint64(binary.BigEndian.Uint32(data[66:70]))
	if !index.IsUint64() || !start.IsUint64() || start.Sign() == 0 || size == 0 || size-1 > ^uint64(0)-start.Uint64() {
		return batch, nil, fatal("invalid Inbox batch range")
	}
	batch = store.BlockBatch{Index: index.Uint64(), Root: b.ParentHash(), Size: size, PrevTotalElements: start.Uint64() - 1, BlockNumber: b.NumberU64(), Timestamp: b.Time(), Submitter: sender, L1TransactionHash: tx.Hash()}
	hashes := []common.Hash{}
	seen := map[common.Hash]bool{}
	for offset := 70; offset < len(data); offset += 32 {
		h := common.BytesToHash(data[offset : offset+32])
		if h == (common.Hash{}) {
			return batch, nil, fatal("zero Blob transaction reference")
		}
		if !seen[h] {
			hashes = append(hashes, h)
			seen[h] = true
		}
	}
	return batch, hashes, nil
}
func (s *Syncer) sourceBlock(ctx context.Context, hash common.Hash) (*types.Block, *types.Transaction, *types.Receipt, error) {
	r, err := s.RPC.TransactionReceipt(ctx, hash)
	if err != nil {
		return nil, nil, nil, err
	}
	if r == nil {
		return nil, nil, nil, fmt.Errorf("blob transaction receipt unavailable; retry")
	}
	b, err := s.RPC.BlockByHash(ctx, r.BlockHash)
	if err != nil {
		return nil, nil, nil, err
	}
	if b == nil {
		return nil, nil, nil, fmt.Errorf("blob transaction block unavailable; retry")
	}
	if !b.Number().IsUint64() || r.BlockNumber == nil || r.BlockNumber.Cmp(b.Number()) != 0 || r.TxHash != hash || r.BlockHash != b.Hash() || uint64(r.TransactionIndex) >= uint64(len(b.Transactions())) {
		return nil, nil, nil, fatal("invalid Blob transaction receipt")
	}
	tx := b.Transactions()[r.TransactionIndex]
	if tx.Hash() != hash || tx.Type() != types.BlobTxType || len(tx.BlobHashes()) == 0 || r.Status != types.ReceiptStatusSuccessful {
		return nil, nil, nil, fatal("invalid referenced Blob transaction")
	}
	return b, tx, r, nil
}
func (s *Syncer) checkSource(tx *types.Transaction, b *types.Block, r *types.Receipt, commit *types.Block, commitIndex uint, authorized common.Address) error {
	sender, err := s.transactionSender(tx)
	if err != nil {
		return err
	}
	if sender != authorized || tx.To() == nil || *tx.To() != s.Config.Inbox {
		return fatal("unauthorized referenced Blob transaction")
	}
	if b.Number().Cmp(commit.Number()) > 0 || (b.Number().Cmp(commit.Number()) == 0 && (b.Hash() != commit.Hash() || r.TransactionIndex > commitIndex)) {
		return fatal("Blob reference is after its Inbox commitment")
	}
	return nil
}
func (s *Syncer) fetchTransaction(ctx context.Context, hash common.Hash, commit *types.Block, index uint, sender common.Address, cutoff uint64) (store.BlobTransaction, error) {
	var out store.BlobTransaction
	b, tx, r, err := s.sourceBlock(ctx, hash)
	if err != nil {
		return out, err
	}
	if err := s.checkSource(tx, b, r, commit, index, sender); err != nil {
		return out, err
	}
	out = store.BlobTransaction{Hash: hash, BlockHash: b.Hash(), Height: b.NumberU64(), Timestamp: b.Time(), TxIndex: r.TransactionIndex}
	if b.Time() < cutoff {
		return out, nil
	}
	frames, err := s.Beacon.Frames(ctx, b.Header(), tx.BlobHashes())
	if errors.Is(err, blob.ErrMissing) {
		out.Missing = true
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Frames = frames
	return out, nil
}
func (s *Syncer) validateSource(ctx context.Context, source store.BlobTransaction, commit *types.Block, index uint, sender common.Address) error {
	b, tx, r, err := s.sourceBlock(ctx, source.Hash)
	if err != nil {
		return err
	}
	if source.BlockHash != b.Hash() || source.Height != b.NumberU64() || source.Timestamp != b.Time() || source.TxIndex != r.TransactionIndex {
		return fatal("cached Blob provenance mismatch")
	}
	return s.checkSource(tx, b, r, commit, index, sender)
}

type scanResult struct {
	txs      []store.BlobTransaction
	channels []store.BlobChannel
	cache    map[common.Hash]store.BlobTransaction
}

func (s *Syncer) submission(ctx context.Context, a *authorization, tx *types.Transaction, block *types.Block, index uint, cutoff uint64, result *scanResult) error {
	if tx.To() == nil || *tx.To() != s.Config.Inbox || len(tx.Data()) == 0 || tx.Data()[0] != 3 {
		return nil
	}
	// Recover and filter the sender before enforcing the submission chain ID.
	// In particular, valid unprotected legacy transactions have chain ID zero.
	sender, err := types.Sender(types.LatestSignerForChainID(number(s.Config.L1ChainID)), tx)
	if err != nil {
		return fatal("invalid Inbox transaction signature")
	}
	if sender != a.sender(block.NumberU64(), false, s.Config.InboxSender) {
		return nil
	}
	if tx.ChainId().Cmp(number(s.Config.L1ChainID)) != 0 {
		return fatal("Inbox transaction L1 chain mismatch")
	}
	receipt, err := s.receipt(ctx, tx.Hash(), block, index)
	if err != nil {
		return err
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return nil
	}
	batch, hashes, err := parseSubmission(tx, block, sender)
	if err != nil {
		return err
	}
	blobSender := a.sender(block.NumberU64(), true, s.Config.BlobSender)
	sources := make([]store.BlobTransaction, 0, len(hashes))
	for _, hash := range hashes {
		source, err := s.resolveSource(ctx, result, hash, block, index, blobSender, cutoff)
		if err != nil {
			return err
		}
		if source.Timestamp >= cutoff {
			sources = append(sources, source)
		}
	}
	decoded, err := blob.DecodeChannels(sources, batch, s.Config.L2ChainID)
	if err != nil {
		return err
	}
	result.channels = append(result.channels, decoded...)
	return nil
}
func (s *Syncer) resolveSource(ctx context.Context, result *scanResult, hash common.Hash, block *types.Block, index uint, sender common.Address, cutoff uint64) (store.BlobTransaction, error) {
	source, known := result.cache[hash]
	if !known {
		saved, err := s.Store.BlobTransaction(hash)
		if err != nil {
			return source, fmt.Errorf("%w: %w", store.ErrIntegrity, err)
		}
		if saved != nil {
			source = *saved
		} else {
			source, err = s.fetchTransaction(ctx, hash, block, index, sender, cutoff)
			if err != nil {
				return source, err
			}
			result.cache[hash] = source
			if source.Timestamp >= cutoff {
				result.txs = append(result.txs, source)
			}
			return source, nil // fetch already checked this reference's authorization
		}
		result.cache[hash] = source
	}
	// Content is cached, but authorization belongs to each Inbox commitment.
	if err := s.validateSource(ctx, source, block, index, sender); err != nil {
		return source, err
	}
	return source, nil
}
