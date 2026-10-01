package catalogruntime

import (
	"sync"
	"time"
)

// SnapshotCache stores one immutable cloneable value with generation-safe
// stale-while-revalidate coordination.
type SnapshotCache[T any] struct {
	mu         sync.RWMutex
	ttl        time.Duration
	clone      func(T) T
	now        func() time.Time
	value      T
	storedAt   time.Time
	set        bool
	expired    bool
	generation uint64
	refreshing bool
}

// NewSnapshotCache constructs an empty cache. clone must return an isolated value.
func NewSnapshotCache[T any](ttl time.Duration, clone func(T) T) *SnapshotCache[T] {
	return NewSnapshotCacheWithClock(ttl, clone, time.Now)
}

// NewSnapshotCacheWithClock constructs a cache with an explicit clock.
func NewSnapshotCacheWithClock[T any](ttl time.Duration, clone func(T) T, now func() time.Time) *SnapshotCache[T] {
	if now == nil {
		now = time.Now
	}
	return &SnapshotCache[T]{ttl: ttl, clone: clone, now: now}
}

// Read reports the value, presence, freshness, and current generation.
func (c *SnapshotCache[T]) Read() (T, bool, bool, uint64) {
	var zero T
	if c == nil {
		return zero, false, false, 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.set {
		return zero, false, false, c.generation
	}
	value := c.value
	if c.clone != nil {
		value = c.clone(value)
	}
	return value, true, !c.expired && c.now().Sub(c.storedAt) < c.ttl, c.generation
}

// Store saves value only when generation still matches the caller's snapshot.
func (c *SnapshotCache[T]) Store(value T, generation uint64) bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if generation != c.generation {
		return false
	}
	if c.clone != nil {
		value = c.clone(value)
	}
	c.value = value
	c.storedAt = c.now()
	c.set = true
	c.expired = false
	return true
}

// Invalidate drops the value and rejects stores from an older generation.
func (c *SnapshotCache[T]) Invalidate() {
	if c == nil {
		return
	}
	c.mu.Lock()
	var zero T
	c.value = zero
	c.storedAt = time.Time{}
	c.set = false
	c.expired = false
	c.generation++
	c.mu.Unlock()
}

// Expire retains the stale value but forces revalidation on the next read.
func (c *SnapshotCache[T]) Expire() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.expired = true
	c.mu.Unlock()
}

// BeginRefresh elects one caller to refresh a stale value.
func (c *SnapshotCache[T]) BeginRefresh() bool {
	if c == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.refreshing {
		return false
	}
	c.refreshing = true
	return true
}

// EndRefresh releases the single-refresh lease.
func (c *SnapshotCache[T]) EndRefresh() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.refreshing = false
	c.mu.Unlock()
}
