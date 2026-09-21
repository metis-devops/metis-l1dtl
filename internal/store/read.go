package store

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/cockroachdb/pebble/v2"
)

func key(n uint64) []byte {
	b := make([]byte, len("enqueue/")+8)
	copy(b, "enqueue/")
	binary.BigEndian.PutUint64(b[len("enqueue/"):], n)
	return b
}

type getter interface {
	Get([]byte) ([]byte, io.Closer, error)
}

func readValue(g getter, key []byte) ([]byte, error) {
	value, closer, err := g.Get(key)
	if errors.Is(err, pebble.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Pebble owns value until the closer is released.
	out := bytes.Clone(value)
	if err := closer.Close(); err != nil {
		return nil, err
	}
	return out, nil
}

func readState(g getter) (State, error) {
	var st State
	raw, err := readValue(g, stateKey)
	if err != nil {
		return st, err
	}
	if raw != nil {
		if err := json.Unmarshal(raw, &st); err != nil {
			return st, fmt.Errorf("%w: %w", ErrIntegrity, err)
		}
	}
	return st, nil
}

func (s *Store) State() (State, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return State{}, pebble.ErrClosed
	}
	return readState(s.db)
}

func (s *Store) Get(index *uint64) (*Enqueue, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, pebble.ErrClosed
	}
	st, err := readState(s.db)
	if err != nil {
		return nil, err
	}
	if index == nil {
		index = st.Latest
	}
	if index == nil {
		return nil, nil
	}
	raw, err := readValue(s.db, key(*index))
	if err != nil {
		return nil, err
	}
	if raw == nil {
		if st.Latest != nil && *index <= *st.Latest {
			return nil, fmt.Errorf("%w: missing committed enqueue %d", ErrIntegrity, *index)
		}
		return nil, nil
	}
	var r Record
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIntegrity, err)
	}
	return &r.Enqueue, nil
}
