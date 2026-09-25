package ingest

import (
	"errors"
	"sync"
)

var ErrFatal = errors.New("synchronization integrity error")

type Status struct {
	mu                     sync.RWMutex
	ready                  bool
	blobEnabled, blobReady bool
	fatal                  error
	halted                 chan struct{}
}

func (s *Status) Snapshot() (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready && (!s.blobEnabled || s.blobReady), s.fatal
}

func (s *Status) EnableBlob() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blobEnabled = true
	s.blobReady = false
}

func (s *Status) SetBlobReady(ready bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.blobReady = ready && s.fatal == nil
}

// CommitIfHealthy shares the fatal-publication barrier with both ingestion loops.
func (s *Status) CommitIfHealthy(commit func() error) error { return s.commitIfHealthy(commit) }

func (s *Status) Set(ready bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil && s.fatal == nil {
		s.fatal = err
		if s.halted == nil {
			s.halted = make(chan struct{})
		}
		close(s.halted)
	}
	s.ready = ready && s.fatal == nil
}

// Done closes on the first fatal error, including errors detected by HTTP reads.
func (s *Status) Done() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.halted == nil {
		s.halted = make(chan struct{})
	}
	return s.halted
}

// commitIfHealthy serializes fatal publication with the durable commit. A commit
// already in progress finishes before the halt is published; none can start after.
// The callback must not call Status methods while this lock is held.
func (s *Status) commitIfHealthy(commit func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fatal != nil {
		return s.fatal
	}
	return commit()
}
