// Package pagedview manages bounded retention and presentation coordinates.
package pagedview

import (
	"errors"
	"sort"
	"sync"
	"time"
)

var (
	ErrBudget = errors.New("presentation budget exhausted")
	// ErrMissing reports a key absent from an index or page. Registry handles
	// never report it; a handle that is not retained is ErrExpired.
	ErrMissing           = errors.New("entry not found")
	ErrExpired           = errors.New("presentation expired")
	ErrPreparing         = errors.New("presentation is being prepared")
	ErrRevision          = errors.New("presentation revision changed")
	ErrOperationConflict = errors.New("operation identity reused with different intent")
)

// Budget accounts for reserved and retained bytes across domain adapters.
type Budget struct {
	mu          sync.Mutex
	limit, used int64
	serial      uint64
	evictors    map[uint64]func() idleCandidate
}

func NewBudget(limit int64) *Budget { return &Budget{limit: max(0, limit)} }

func (b *Budget) Reserve(bytes int64) (*Reservation, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bytes < 0 || bytes > b.limit-b.used {
		return nil, ErrBudget
	}
	b.used += bytes
	return &Reservation{budget: b, bytes: bytes}, nil
}

func (b *Budget) Used() int64 { b.mu.Lock(); defer b.mu.Unlock(); return b.used }

// Reservation remains charged until the retained object and its active readers release it.
type Reservation struct {
	mu     sync.Mutex
	budget *Budget
	bytes  int64
	closed bool
}

func (r *Reservation) Resize(bytes int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return ErrExpired
	}
	r.budget.mu.Lock()
	defer r.budget.mu.Unlock()
	delta := bytes - r.bytes
	if bytes < 0 || delta > r.budget.limit-r.budget.used {
		return ErrBudget
	}
	r.budget.used += delta
	r.bytes = bytes
	return nil
}

func (r *Reservation) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.budget.mu.Lock()
	r.budget.used -= r.bytes
	r.budget.mu.Unlock()
	r.closed = true
}

// idleCandidate is revalidated by evict after selection; a concurrent Acquire
// can pin it between selection and eviction.
type idleCandidate struct {
	used  time.Time
	evict func() bool
}

func (b *Budget) registerEvictor(candidate func() idleCandidate) func() {
	b.mu.Lock()
	b.serial++
	id := b.serial
	if b.evictors == nil {
		b.evictors = make(map[uint64]func() idleCandidate)
	}
	b.evictors[id] = candidate
	b.mu.Unlock()
	return func() { b.mu.Lock(); delete(b.evictors, id); b.mu.Unlock() }
}
func (b *Budget) evictIdle() bool {
	b.mu.Lock()
	sources := make([]func() idleCandidate, 0, len(b.evictors))
	for _, candidate := range b.evictors {
		sources = append(sources, candidate)
	}
	b.mu.Unlock()
	candidates := make([]idleCandidate, 0, len(sources))
	for _, source := range sources {
		next := source()
		if next.evict != nil {
			candidates = append(candidates, next)
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].used.Before(candidates[j].used) })
	for _, candidate := range candidates {
		if candidate.evict() {
			return true
		}
	}
	return false
}

func (b *Budget) canFit(bytes int64) bool { return bytes >= 0 && bytes <= b.limit }

// ReserveEvictingIdle reclaims idle reservations outside registry locks; evictors recheck pins.
func (b *Budget) ReserveEvictingIdle(bytes int64) (*Reservation, error) {
	if !b.canFit(bytes) {
		return nil, ErrBudget
	}
	for {
		reservation, err := b.Reserve(bytes)
		if err == nil || !b.evictIdle() {
			return reservation, err
		}
	}
}
