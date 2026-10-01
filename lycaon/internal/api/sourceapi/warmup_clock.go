package sourceapi

import (
	"sync"
	"time"
)

// Retry delays grow with elapsed warm-up time within these bounds.
const (
	warmupRetryMin = 150 * time.Millisecond
	warmupRetryMax = 2 * time.Second
	// Next wait as a fraction of elapsed time.
	warmupRetryDivisor = 4
)

// warmupRetryAfterMS is the retry hint for work that has run for elapsed.
func warmupRetryAfterMS(elapsed time.Duration) int {
	next := elapsed / warmupRetryDivisor
	if next < warmupRetryMin {
		next = warmupRetryMin
	}
	if next > warmupRetryMax {
		next = warmupRetryMax
	}
	return int(next / time.Millisecond)
}

// warmupClock shares per-resource start times across warming endpoints.
type warmupClock struct {
	mu    sync.Mutex
	since map[string]time.Time
	now   func() time.Time
}

func newWarmupClock() *warmupClock {
	return &warmupClock{since: make(map[string]time.Time), now: time.Now}
}

// warming starts the resource clock on first use; a nil clock returns the minimum delay.
func (c *warmupClock) warming(key string) int {
	if c == nil {
		return warmupRetryAfterMS(0)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	started, ok := c.since[key]
	if !ok {
		c.since[key] = now
		return warmupRetryAfterMS(0)
	}
	return warmupRetryAfterMS(now.Sub(started))
}

// ready clears key's clock so a later warm-up starts brisk again.
func (c *warmupClock) ready(key string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.since, key)
}
