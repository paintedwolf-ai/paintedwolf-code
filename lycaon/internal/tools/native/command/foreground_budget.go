package command

import (
	"sync"
	"time"

	"github.com/lycaon/lycaon/internal/commandprogram"
)

// foregroundBudget is the run time a program may hold its call before it is
// exposed as a background job. Time waiting on a person does not count.
type foregroundBudget struct {
	mu        sync.Mutex
	clock     commandprogram.Clock
	remaining time.Duration
	started   time.Time
	stop      func() bool
	holds     int
	done      bool
	expired   chan struct{}
}

func newForegroundBudget(clock commandprogram.Clock, budget time.Duration) *foregroundBudget {
	b := &foregroundBudget{clock: clock, remaining: budget, expired: make(chan struct{})}
	b.mu.Lock()
	b.resumeLocked()
	b.mu.Unlock()
	return b
}

// Expired closes once the budget is spent.
func (b *foregroundBudget) Expired() <-chan struct{} { return b.expired }

// BeginHumanWait pauses the budget until every overlapping wait has ended.
func (b *foregroundBudget) BeginHumanWait() func() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return func() {}
	}
	b.holds++
	if b.holds == 1 && b.stop != nil && b.stop() {
		b.remaining -= b.clock.Now().Sub(b.started)
		b.stop = nil
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			defer b.mu.Unlock()
			b.holds--
			if b.holds == 0 && !b.done {
				b.resumeLocked()
			}
		})
	}
}

// Close stops the budget; it never expires afterwards.
func (b *foregroundBudget) Close() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.done = true
	if b.stop != nil {
		b.stop()
		b.stop = nil
	}
}

func (b *foregroundBudget) resumeLocked() {
	b.started = b.clock.Now()
	b.stop = b.clock.AfterFunc(max(b.remaining, 0), b.expire)
}

func (b *foregroundBudget) expire() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.done {
		return
	}
	b.done = true
	b.stop = nil
	close(b.expired)
}
