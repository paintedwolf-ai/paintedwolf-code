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

// programWaits routes one program's waits on a person to its foreground
// budget and, once the program runs in the background, to its job.
type programWaits struct {
	mu     sync.Mutex
	budget *foregroundBudget
	job    func() (end func())
	open   int
	endJob func()
}

func (w *programWaits) BeginHumanWait() func() {
	endBudget := w.budget.BeginHumanWait()
	w.mu.Lock()
	w.open++
	if w.open == 1 && w.job != nil {
		w.endJob = w.job()
	}
	w.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			endBudget()
			w.mu.Lock()
			defer w.mu.Unlock()
			w.open--
			if w.open == 0 && w.endJob != nil {
				w.endJob()
				w.endJob = nil
			}
		})
	}
}

// promoted reports later waits, and any open now, to the program's job.
func (w *programWaits) promoted(job func() (end func())) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.job = job
	if w.open > 0 {
		w.endJob = job()
	}
}
