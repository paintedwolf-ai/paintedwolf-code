package actionoutcome

import (
	"context"
	"errors"
	"slices"
	"sync"
)

var ErrUnavailable = errors.New("outcome unavailable")

// History retains live records and the most recently settled records.
type History[T any] struct {
	mu      sync.Mutex
	limit   int
	records map[string]*entry[T]
	settled []string
}

type entry[T any] struct {
	value    T
	done     chan struct{}
	terminal bool
}

func NewHistory[T any](limit int) *History[T] {
	return &History[T]{limit: limit, records: make(map[string]*entry[T])}
}

func (h *History[T]) Open(id string, value T) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, exists := h.records[id]; exists {
		return false
	}
	h.records[id] = &entry[T]{value: value, done: make(chan struct{})}
	return true
}

// Change publishes a value and wakes waiters under the same lock.
func (h *History[T]) Change(id string, apply func(T) (T, bool)) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	e := h.records[id]
	if e == nil || e.terminal {
		return false
	}
	e.value, e.terminal = apply(e.value)
	if e.terminal {
		close(e.done)
		h.settled = append(h.settled, id)
		if len(h.settled) > h.limit {
			delete(h.records, h.settled[0])
			h.settled = slices.Delete(h.settled, 0, 1)
		}
	}
	return true
}

func (h *History[T]) Await(ctx context.Context, id string) (T, error) {
	h.mu.Lock()
	e := h.records[id]
	h.mu.Unlock()
	var zero T
	if e == nil {
		return zero, ErrUnavailable
	}
	select {
	case <-e.done:
		return e.value, nil
	case <-ctx.Done():
		return zero, ctx.Err()
	}
}

func (h *History[T]) Snapshot() []T {
	h.mu.Lock()
	defer h.mu.Unlock()
	values := make([]T, 0, len(h.records))
	for _, e := range h.records {
		values = append(values, e.value)
	}
	return values
}
