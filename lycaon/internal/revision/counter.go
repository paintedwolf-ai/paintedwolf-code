// Package revision provides per-key counters for digest cache invalidation.
package revision

import (
	"strings"
	"sync"
)

// Counter tracks a monotonic revision per key (typically a root session id).
type Counter struct {
	mu    sync.Mutex
	byKey map[string]uint64
}

// NewCounter returns an empty counter.
func NewCounter() *Counter {
	return &Counter{byKey: make(map[string]uint64)}
}

// Get returns the current revision for key.
func (c *Counter) Get(key string) uint64 {
	if c == nil {
		return 0
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byKey[key]
}

// Bump increments and returns the revision for key.
func (c *Counter) Bump(key string) uint64 {
	if c == nil {
		return 0
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byKey[key]++
	return c.byKey[key]
}
