package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cockroachdb/pebble/v2"
)

func (s *Store) initIdentity(id Identity) error {
	raw, err := json.Marshal(id)
	if err != nil {
		return err
	}
	old, err := readValue(s.db, identityKey)
	if err != nil {
		return err
	}
	if old != nil {
		if !bytes.Equal(old, raw) {
			return fmt.Errorf("%w: database identity mismatch", ErrIntegrity)
		}
		return nil
	}
	// Never claim an existing database whose identity was lost or never belonged
	// to this service. An empty database is safe to initialize after a crash.
	iter, err := s.db.NewIter(nil)
	if err != nil {
		return err
	}
	nonempty := iter.First()
	err = errors.Join(iter.Error(), iter.Close())
	if err != nil {
		return err
	}
	if nonempty {
		return fmt.Errorf("%w: missing database identity", ErrIntegrity)
	}
	return s.db.Set(identityKey, raw, pebble.Sync)
}
