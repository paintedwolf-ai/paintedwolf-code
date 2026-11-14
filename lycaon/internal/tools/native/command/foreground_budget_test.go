package command

import (
	"sync"
	"testing"
	"time"
)

type budgetClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[int]budgetTimer
	next   int
}

type budgetTimer struct {
	at time.Time
	f  func()
}

func (c *budgetClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *budgetClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	id := c.next
	c.timers[id] = budgetTimer{at: c.now.Add(d), f: f}
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, pending := c.timers[id]
		delete(c.timers, id)
		return pending
	}
}

func (c *budgetClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []func()
	for id, timer := range c.timers {
		if !timer.at.After(c.now) {
			due = append(due, timer.f)
			delete(c.timers, id)
		}
	}
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
}

func expired(b *foregroundBudget) bool {
	select {
	case <-b.Expired():
		return true
	default:
		return false
	}
}

// Only run time spends the budget: overlapping waits on a person pause it
// once, and it resumes with what was left.
func TestForegroundBudgetCountsOnlyRunTime(t *testing.T) {
	clock := &budgetClock{now: time.Unix(0, 0), timers: map[int]budgetTimer{}}
	b := newForegroundBudget(clock, 30*time.Second)
	clock.advance(20 * time.Second)
	endFirst := b.BeginHumanWait()
	endSecond := b.BeginHumanWait()
	clock.advance(time.Hour)
	endFirst()
	endFirst()
	clock.advance(time.Hour)
	if expired(b) {
		t.Fatal("budget spent while a person was still deciding")
	}
	endSecond()
	clock.advance(9 * time.Second)
	if expired(b) {
		t.Fatal("budget spent before its remaining run time")
	}
	clock.advance(time.Second)
	if !expired(b) {
		t.Fatal("budget not spent after its full run time")
	}
	b.BeginHumanWait()()
	if len(clock.timers) != 0 {
		t.Fatalf("spent budget rescheduled: %d timers", len(clock.timers))
	}
	closed := newForegroundBudget(clock, time.Second)
	closed.Close()
	closed.BeginHumanWait()()
	clock.advance(time.Minute)
	if expired(closed) || len(clock.timers) != 0 {
		t.Fatal("closed budget expired or rescheduled")
	}
}

// After promotion, a program's waits on a person mark its job; one open at
// promotion is reported from then.
func TestProgramWaitsFollowTheProgramIntoTheBackground(t *testing.T) {
	clock := &budgetClock{now: time.Unix(0, 0), timers: map[int]budgetTimer{}}
	waits := &programWaits{budget: newForegroundBudget(clock, 30*time.Second)}
	begun, ended := 0, 0
	job := func() func() {
		begun++
		return func() { ended++ }
	}
	endOpen := waits.BeginHumanWait()
	waits.promoted(job)
	if begun != 1 || ended != 0 {
		t.Fatalf("open wait at promotion: begun=%d ended=%d", begun, ended)
	}
	endOverlap := waits.BeginHumanWait()
	endOpen()
	if begun != 1 || ended != 0 {
		t.Fatalf("overlapping wait reached the job separately: begun=%d ended=%d", begun, ended)
	}
	endOverlap()
	waits.BeginHumanWait()()
	if begun != 2 || ended != 2 {
		t.Fatalf("job waits: begun=%d ended=%d, want 2 and 2", begun, ended)
	}
}
