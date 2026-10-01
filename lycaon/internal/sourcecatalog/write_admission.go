package sourcecatalog

import (
	"context"

	"github.com/lycaon/lycaon/internal/pagedview"
)

func (s *indexStore) writerGate() chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writer == nil {
		s.writer = make(chan struct{}, 1)
	}
	return s.writer
}

// write serializes cache transactions before they open read snapshots.
// Readers remain independent under WAL; no writer waits while holding I/O admission.
func (s *indexStore) write(ctx context.Context) (func(), error) {
	s.mu.Lock()
	if s.retired {
		s.mu.Unlock()
		return nil, pagedview.ErrExpired
	}
	if s.writer == nil {
		s.writer = make(chan struct{}, 1)
	}
	gate := s.writer
	s.mu.Unlock()
	select {
	case gate <- struct{}{}:
		s.mu.Lock()
		retired := s.retired
		s.mu.Unlock()
		if retired {
			<-gate
			return nil, pagedview.ErrExpired
		}
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *indexStore) lockWriter(ctx context.Context) (func(), error) {
	gate := s.writerGate()
	select {
	case gate <- struct{}{}:
		return func() { <-gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
