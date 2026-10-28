package programtest

import (
	"sort"
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// ManualClock is a program clock that moves only when a test advances it.
type ManualClock struct {
	mu      sync.Mutex
	now     time.Time
	next    int
	pending map[int]manualTimer
}

type manualTimer struct {
	at time.Time
	f  func()
}

var _ commandprogram.Clock = (*ManualClock)(nil)

// NewManualClock starts a clock at a fixed instant.
func NewManualClock() *ManualClock {
	return &ManualClock{now: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), pending: map[int]manualTimer{}}
}

func (c *ManualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *ManualClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	id := c.next
	c.pending[id] = manualTimer{at: c.now.Add(d), f: f}
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, pending := c.pending[id]
		delete(c.pending, id)
		return pending
	}
}

// Pending reports how many timers wait to fire.
func (c *ManualClock) Pending() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

// Advance moves the clock forward and runs every timer now due, in due order.
func (c *ManualClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []manualTimer
	for id, timer := range c.pending {
		if !timer.at.After(c.now) {
			due = append(due, timer)
			delete(c.pending, id)
		}
	}
	c.mu.Unlock()
	sort.SliceStable(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
	for _, timer := range due {
		timer.f()
	}
}
