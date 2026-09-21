package ingest

import (
	"errors"
	"sync"
)

var ErrFatal = errors.New("synchronization integrity error")

type Status struct {
	mu     sync.RWMutex
	ready  bool
	fatal  error
	halted chan struct{}
}

func (s *Status) Snapshot() (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ready, s.fatal
}

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
