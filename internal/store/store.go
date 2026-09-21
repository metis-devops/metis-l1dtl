package store

import (
	"os"
	"sync"

	"github.com/cockroachdb/pebble/v2"
)

// Store serializes checkpoint comparisons and commits. Readers hold a shared
// lock so latest-index lookup and record lookup see the same committed state.
type Store struct {
	db     *pebble.DB
	mu     sync.RWMutex
	closed bool
}

func Open(path string, id Identity) (*Store, error) {
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	db, err := pebble.Open(path, &pebble.Options{})
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err = s.initIdentity(id); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.db.Close()
}
