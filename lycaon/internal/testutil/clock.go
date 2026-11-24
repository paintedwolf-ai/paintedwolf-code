package testutil

import (
	"sync"
	"time"
)

// FakeClock is a deterministic manual UTC clock for tests.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func NewFakeClock(initial time.Time) *FakeClock {
	return &FakeClock{now: initial.UTC()}
}

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *FakeClock) Set(t time.Time) {
	c.mu.Lock()
	c.now = t.UTC()
	c.mu.Unlock()
}

func (c *FakeClock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	c.now = c.now.Add(d)
	now := c.now
	c.mu.Unlock()
	return now
}
