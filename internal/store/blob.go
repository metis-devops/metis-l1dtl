package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"

	"github.com/cockroachdb/pebble/v2"
	"github.com/ethereum/go-ethereum/common"
)

var blobIdentityKey = []byte("blob/identity")
var blobStateKey = []byte("blob/state")

func blobKey(kind string, suffix []byte) []byte { return append([]byte("blob/"+kind+"/"), suffix...) }
func blobNumberKey(kind string, n uint64) []byte {
	return blobKey(kind, binary.BigEndian.AppendUint64(nil, n))
}
func expirationKey(kind string, ts uint64, id []byte) []byte {
	return append(blobNumberKey("expiry/"+kind, ts), id...)
}

func readJSON(g getter, key []byte, dst any, required bool) (bool, error) {
	raw, err := readValue(g, key)
	if err != nil {
		return false, err
	}
	if raw == nil {
		if required {
			return false, fmt.Errorf("%w: missing %s", ErrIntegrity, key)
		}
		return false, nil
	}
	if err := json.Unmarshal(raw, dst); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return false, fmt.Errorf("%w: invalid %s", ErrIntegrity, key)
	}
	return true, nil
}
func putJSON(b *pebble.Batch, key []byte, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return b.Set(key, raw, nil)
}
func readBlobState(g getter) (BlobState, error) {
	var st BlobState
	_, err := readJSON(g, blobStateKey, &st, true)
	return st, err
}

func (s *Store) InitBlob(id BlobIdentity) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return pebble.ErrClosed
	}
	var existing BlobIdentity
	ok, err := readJSON(s.db, blobIdentityKey, &existing, false)
	if err != nil {
		return err
	}
	if ok {
		if existing != id {
			return fmt.Errorf("%w: Blob database identity mismatch", ErrIntegrity)
		}
		_, err = readBlobState(s.db)
		return err
	}
	it, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte("blob/"), UpperBound: []byte("blob0")})
	if err != nil {
		return err
	}
	nonempty := it.First()
	iterErr := it.Error()
	closeErr := it.Close()
	if iterErr != nil {
		return iterErr
	}
	if closeErr != nil {
		return closeErr
	}
	if nonempty {
		return fmt.Errorf("%w: missing Blob identity", ErrIntegrity)
	}
	b := s.db.NewBatch()
	defer b.Close() //nolint:errcheck
	if err := putJSON(b, blobIdentityKey, id); err != nil {
		return err
	}
	if err := putJSON(b, blobStateKey, BlobState{}); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}

func (s *Store) BlobState() (BlobState, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return BlobState{}, pebble.ErrClosed
	}
	return readBlobState(s.db)
}
func (s *Store) BlobTransaction(hash common.Hash) (*BlobTransaction, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, pebble.ErrClosed
	}
	var tx BlobTransaction
	ok, err := readJSON(s.db, blobKey("tx", hash[:]), &tx, false)
	if err != nil || !ok {
		return nil, err
	}
	if tx.Hash != hash {
		return nil, ErrIntegrity
	}
	return &tx, nil
}

// CommitBlob compares the entire metadata snapshot, including the retention
// revision. Pruning and scanner commits cannot overwrite one another.
func (s *Store) CommitBlob(before, after BlobState, txs []BlobTransaction, channels []BlobChannel) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return pebble.ErrClosed
	}
	actual, err := readBlobState(s.db)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, before) {
		return fmt.Errorf("%w: stale Blob checkpoint", ErrIntegrity)
	}
	if before.Revision == ^uint64(0) || after.Cutoff < before.Cutoff {
		return ErrIntegrity
	}
	b := s.db.NewIndexedBatch()
	defer b.Close() //nolint:errcheck
	for _, tx := range txs {
		if tx.Timestamp < after.Cutoff {
			continue
		}
		if tx.Missing && len(tx.Frames) != 0 {
			return ErrIntegrity
		}
		if err := putIdentical(b, blobKey("tx", tx.Hash[:]), tx); err != nil {
			return err
		}
		if err := b.Set(expirationKey("tx", tx.Timestamp, tx.Hash[:]), nil, nil); err != nil {
			return err
		}
	}
	for _, ch := range channels {
		if err := putChannel(b, ch, after.Cutoff); err != nil {
			return err
		}
	}
	if err := pruneBlob(b, after.Cutoff); err != nil {
		return err
	}
	latest, err := latestBlockIndex(b)
	if err != nil {
		return err
	}
	after.Latest = latest
	after.Revision = before.Revision + 1
	if err := putJSON(b, blobStateKey, after); err != nil {
		return err
	}
	return b.Commit(pebble.Sync)
}

func putIdentical(b *pebble.Batch, key []byte, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	old, err := readValue(b, key)
	if err != nil {
		return err
	}
	if old != nil && !bytes.Equal(old, raw) {
		return fmt.Errorf("%w: conflicting Blob record", ErrIntegrity)
	}
	return b.Set(key, raw, nil)
}

func putChannel(b *pebble.Batch, ch BlobChannel, cutoff uint64) error {
	r := ch.Record
	if len(r.Sources) == 0 || len(r.Blocks) != len(ch.Blocks) || len(ch.Blocks) == 0 || r.ID == "" {
		return ErrIntegrity
	}
	oldest := ^uint64(0)
	for _, hash := range r.Sources {
		var tx BlobTransaction
		if _, err := readJSON(b, blobKey("tx", hash[:]), &tx, true); err != nil {
			return err
		}
		if tx.Missing || tx.Hash != hash {
			return ErrIntegrity
		}
		if tx.Timestamp < oldest {
			oldest = tx.Timestamp
		}
	}
	if r.ExpiresAt != oldest {
		return ErrIntegrity
	}
	if oldest < cutoff {
		return nil
	}
	if err := putIdentical(b, blobKey("channel", []byte(r.ID)), r); err != nil {
		return err
	}
	for i, block := range ch.Blocks {
		if block.Index != r.Blocks[i] || block.BatchIndex != r.Batch.Index {
			return ErrIntegrity
		}
		if err := putIdentical(b, blobNumberKey("index", block.Index), r.ID); err != nil {
			return err
		}
		if err := putIdentical(b, blobNumberKey("block", block.Index), block); err != nil {
			return err
		}
	}
	return b.Set(expirationKey("channel", oldest, []byte(r.ID)), nil, nil)
}

func pruneBlob(b *pebble.Batch, cutoff uint64) error {
	for _, kind := range []string{"channel", "tx"} {
		if err := pruneKind(b, kind, cutoff); err != nil {
			return err
		}
	}
	return nil
}
func pruneKind(b *pebble.Batch, kind string, cutoff uint64) error {
	lower := blobKey("expiry/"+kind, nil)
	upper := blobNumberKey("expiry/"+kind, cutoff)
	it, err := b.NewIter(&pebble.IterOptions{LowerBound: lower, UpperBound: upper})
	if err != nil {
		return err
	}
	defer it.Close() //nolint:errcheck
	for ok := it.First(); ok; ok = it.Next() {
		k := bytes.Clone(it.Key())
		if len(k) < len(lower)+8 {
			return ErrIntegrity
		}
		id := k[len(lower)+8:]
		timestamp := binary.BigEndian.Uint64(k[len(lower):])
		if err := expireRecord(b, kind, id, timestamp); err != nil {
			return err
		}
		if err := b.Delete(k, nil); err != nil {
			return err
		}
	}
	return it.Error()
}
func expireRecord(b *pebble.Batch, kind string, id []byte, timestamp uint64) error {
	if kind == "channel" {
		var r ChannelRecord
		if _, err := readJSON(b, blobKey(kind, id), &r, true); err != nil {
			return err
		}
		if r.ID != string(id) || r.ExpiresAt != timestamp {
			return ErrIntegrity
		}
		for _, index := range r.Blocks {
			var pointer string
			if _, err := readJSON(b, blobNumberKey("index", index), &pointer, true); err != nil {
				return err
			}
			if pointer != r.ID {
				return ErrIntegrity
			}
			var block Block
			if _, err := readJSON(b, blobNumberKey("block", index), &block, true); err != nil {
				return err
			}
			if block.Index != index {
				return ErrIntegrity
			}
			if err := b.Delete(blobNumberKey("index", index), nil); err != nil {
				return err
			}
			if err := b.Delete(blobNumberKey("block", index), nil); err != nil {
				return err
			}
		}
	} else {
		var tx BlobTransaction
		if _, err := readJSON(b, blobKey(kind, id), &tx, true); err != nil {
			return err
		}
		if !bytes.Equal(tx.Hash[:], id) || tx.Timestamp != timestamp {
			return ErrIntegrity
		}
	}
	return b.Delete(blobKey(kind, id), nil)
}
func latestBlockIndex(b *pebble.Batch) (*uint64, error) {
	p := blobKey("index", nil)
	it, err := b.NewIter(&pebble.IterOptions{LowerBound: p, UpperBound: []byte("blob/index0")})
	if err != nil {
		return nil, err
	}
	var latest *uint64
	if it.Last() {
		if len(it.Key()) != len(p)+8 {
			_ = it.Close()
			return nil, ErrIntegrity
		}
		n := binary.BigEndian.Uint64(it.Key()[len(p):])
		latest = &n
	}
	err = it.Error()
	closeErr := it.Close()
	if err != nil {
		return nil, err
	}
	return latest, closeErr
}

func (s *Store) GetBlock(index *uint64) (BlockResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out BlockResponse
	if s.closed {
		return out, pebble.ErrClosed
	}
	st, err := readBlobState(s.db)
	if err != nil {
		return out, err
	}
	latest := index == nil
	if latest {
		index = st.Latest
	}
	if index == nil {
		return out, nil
	}
	var id string
	ok, err := readJSON(s.db, blobNumberKey("index", *index), &id, latest)
	if err != nil || !ok {
		return out, err
	}
	var r ChannelRecord
	if _, err = readJSON(s.db, blobKey("channel", []byte(id)), &r, true); err != nil {
		return out, err
	}
	var block Block
	if _, err = readJSON(s.db, blobNumberKey("block", *index), &block, true); err != nil {
		return out, err
	}
	found := slices.Contains(r.Blocks, *index)
	if !found || r.ID != id || block.Index != *index || block.BatchIndex != r.Batch.Index || r.ExpiresAt < st.Cutoff {
		return out, ErrIntegrity
	}
	if len(r.Sources) == 0 {
		return out, ErrIntegrity
	}
	for _, hash := range r.Sources {
		var tx BlobTransaction
		if _, err := readJSON(s.db, blobKey("tx", hash[:]), &tx, true); err != nil {
			return out, err
		}
		if tx.Hash != hash || tx.Missing || tx.Timestamp < st.Cutoff {
			return out, ErrIntegrity
		}
	}
	// Match TypeScript TransportDB._getFullBlock while holding one read lock
	// across the Blob and enqueue reads. Never publish deposit placeholders.
	for i := range block.Transactions {
		tx := &block.Transactions[i]
		if tx.QueueOrigin != "l1" {
			continue
		}
		if tx.QueueIndex == nil || *tx.QueueIndex == ^uint64(0) {
			return out, ErrIntegrity
		}
		queueIndex := *tx.QueueIndex
		// TypeScript skips Andromeda's failed enqueue 20397 in block reads.
		if queueIndex >= 20397 {
			queueIndex++
		}
		enqueue, err := readEnqueue(s.db, &queueIndex)
		if err != nil || enqueue == nil {
			return out, err
		}
		tx.BlockNumber = enqueue.BlockNumber
		if tx.Timestamp == 0 {
			tx.Timestamp = enqueue.Timestamp
		}
		tx.GasLimit, tx.Target, tx.Origin, tx.Data = enqueue.GasLimit, enqueue.Target, enqueue.Origin, enqueue.Data
		tx.QueueIndex = &queueIndex
	}
	out.Block, out.Batch = &block, &r.Batch
	return out, nil
}
