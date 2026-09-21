package store

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

func (s *Store) Commit(before, after State, records []Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return pebble.ErrClosed
	}
	actual, err := readState(s.db)
	if err != nil {
		return err
	}
	a, _ := json.Marshal(actual)
	b, _ := json.Marshal(before)
	if !bytes.Equal(a, b) {
		return fmt.Errorf("%w: stale checkpoint", ErrIntegrity)
	}
	// Indexed batches make records staged earlier in this commit visible to
	// duplicate/conflict checks, without exposing them to other readers.
	batch := s.db.NewIndexedBatch()
	defer func() { _ = batch.Close() }()
	after.Latest = before.Latest
	for _, r := range records {
		raw, err := json.Marshal(r)
		if err != nil {
			return err
		}
		old, err := readValue(batch, key(r.Index))
		if err != nil {
			return err
		}
		if old != nil {
			if !bytes.Equal(old, raw) {
				return fmt.Errorf("%w: conflicting enqueue %d", ErrIntegrity, r.Index)
			}
			continue
		}
		expected := uint64(0)
		if after.Latest != nil {
			if *after.Latest == ^uint64(0) {
				return ErrIntegrity
			}
			expected = *after.Latest + 1
		}
		if r.Index != expected {
			return fmt.Errorf("%w: expected queue %d, got %d", ErrIntegrity, expected, r.Index)
		}
		if err := batch.Set(key(r.Index), raw, nil); err != nil {
			return err
		}
		n := r.Index
		after.Latest = &n
	}
	raw, err := json.Marshal(after)
	if err != nil {
		return err
	}
	if err := batch.Set(stateKey, raw, nil); err != nil {
		return err
	}
	// Sync the WAL before reporting the checkpoint as durable.
	return batch.Commit(pebble.Sync)
}
