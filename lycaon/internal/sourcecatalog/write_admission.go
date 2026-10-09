package sourcecatalog

import (
	"context"
	"sync"

	"github.com/lycaon/lycaon/internal/pagedview"
)

// writeAdmission serializes cache transactions before they open read snapshots.
// Retirement is checked again after admission; WAL readers remain independent.
type writeAdmission struct {
	once sync.Once
	gate chan struct{}
}

func (w *writeAdmission) Lock(ctx context.Context) (func(), error) {
	w.once.Do(func() { w.gate = make(chan struct{}, 1) })
	select {
	case w.gate <- struct{}{}:
		return func() { <-w.gate }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (w *writeAdmission) Write(ctx context.Context, writable func() bool) (func(), error) {
	if !writable() {
		return nil, pagedview.ErrExpired
	}
	release, err := w.Lock(ctx)
	if err != nil {
		return nil, err
	}
	if !writable() {
		release()
		return nil, pagedview.ErrExpired
	}
	return release, nil
}

func (s *storeCore) writable() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.retired
}
