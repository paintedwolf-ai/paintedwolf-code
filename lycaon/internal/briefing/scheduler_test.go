package briefing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// gate holds jobs until released, so ordering is controlled without timing.
type gate struct {
	mu      sync.Mutex
	started []string
	release map[string]chan struct{}
	ran     chan string
}

func newGate() *gate { return &gate{release: map[string]chan struct{}{}, ran: make(chan string, 256)} }

func (g *gate) job(key, project string, manual bool, policy Policy) Job {
	g.mu.Lock()
	g.release[key] = make(chan struct{})
	ch := g.release[key]
	g.mu.Unlock()
	return Job{Key: key, Project: project, Manual: manual, Policy: policy, Run: func(ctx context.Context) {
		g.mu.Lock()
		g.started = append(g.started, key)
		g.mu.Unlock()
		g.ran <- key
		select {
		case <-ch:
		case <-ctx.Done():
		}
	}}
}

func (g *gate) open(key string) { close(g.release[key]) }

func (g *gate) next(t *testing.T) string {
	t.Helper()
	return receive(t, g.ran)
}

// receive waits for a value, failing only when the test's own deadline nears.
func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	var expired <-chan time.Time
	if deadline, ok := t.Deadline(); ok {
		expired = time.After(time.Until(deadline) - 5*time.Second)
	}
	select {
	case value := <-ch:
		return value
	case <-expired:
		t.Fatal("expected event never happened")
		var zero T
		return zero
	}
}

func testScheduler(t *testing.T) *Scheduler {
	t.Helper()
	s := NewScheduler(context.Background())
	t.Cleanup(func() { s.Stop(); s.Wait(context.Background()) })
	return s
}

func testPolicy() Policy {
	return Policy{Admission: Queued, QueueLimit: 3, Active: 2, QueueWait: time.Minute}
}

func TestQueuedWorkRespectsActiveSlotsAndQueueBound(t *testing.T) {
	s := testScheduler(t)
	g := newGate()
	for _, key := range []string{"a", "b", "c", "d", "e"} {
		if _, err := s.Submit(t.Context(), g.job(key, "p", false, testPolicy())); err != nil {
			t.Fatalf("submit %s: %v", key, err)
		}
	}
	if _, err := s.Submit(t.Context(), g.job("f", "p", false, testPolicy())); !errors.Is(err, ErrBusy) {
		t.Fatalf("queue past its bound admitted: %v", err)
	}
	if _, err := s.Submit(t.Context(), g.job("other", "q", false, testPolicy())); err != nil {
		t.Fatalf("another project's queue was shared: %v", err)
	}
	started := map[string]bool{g.next(t): true, g.next(t): true, g.next(t): true}
	if !started["a"] || !started["b"] || !started["other"] {
		t.Fatalf("started %v; want a and b for p, and other for q", started)
	}
	g.open("a")
	if got := g.next(t); got != "c" {
		t.Fatalf("queue order broke: %s", got)
	}
}

func TestManualWorkStartsAheadOfAutomaticAndJoinsByKey(t *testing.T) {
	s := testScheduler(t)
	g := newGate()
	policy := testPolicy()
	policy.Active = 1
	_, _ = s.Submit(t.Context(), g.job("running", "p", false, policy))
	g.next(t)
	_, _ = s.Submit(t.Context(), g.job("auto-1", "p", false, policy))
	_, _ = s.Submit(t.Context(), g.job("auto-2", "p", false, policy))
	if joined, _ := s.Submit(t.Context(), g.job("auto-2", "p", true, policy)); !joined {
		t.Fatal("equal key did not join")
	}
	g.open("running")
	if got := g.next(t); got != "auto-2" {
		t.Fatalf("manual promotion lost: %s ran first", got)
	}
}

func TestQueuedWorkExpiresByInjectedClock(t *testing.T) {
	s := testScheduler(t)
	clock := time.Unix(1000, 0)
	s.now = func() time.Time { return clock }
	g := newGate()
	policy := testPolicy()
	policy.Active = 1
	_, _ = s.Submit(t.Context(), g.job("running", "p", false, policy))
	g.next(t)
	expired := make(chan struct{})
	waiting := g.job("waiting", "p", false, policy)
	waiting.Expired = func(context.Context) { close(expired) }
	_, _ = s.Submit(t.Context(), waiting)
	clock = clock.Add(policy.QueueWait + time.Second)
	s.ExpireQueued("p")
	receive(t, expired)
	if joined, err := s.Submit(t.Context(), g.job("waiting", "p", false, policy)); joined || err != nil {
		t.Fatalf("expired key still held: joined=%v err=%v", joined, err)
	}
}

func TestSupersedingCancelsEarlierAutomaticWorkInItsGroup(t *testing.T) {
	s := testScheduler(t)
	cancelled := make(chan string, 4)
	started := make(chan struct{}, 4)
	job := func(key string, manual bool) Job {
		return Job{Key: key, Project: "p", Group: "p", Manual: manual, Policy: Policy{Admission: Superseding}, Run: func(ctx context.Context) {
			started <- struct{}{}
			<-ctx.Done()
			cancelled <- key
		}}
	}
	_, _ = s.Submit(t.Context(), job("first", false))
	receive(t, started)
	_, _ = s.Submit(t.Context(), job("manual", true))
	receive(t, started)
	_, _ = s.Submit(t.Context(), job("second", false))
	receive(t, started)
	if got := receive(t, cancelled); got != "first" {
		t.Fatalf("supersession cancelled %s", got)
	}
	s.Stop()
	s.Wait(context.Background())
}

func TestStopCancelsRunningWorkAndClosesAdmission(t *testing.T) {
	s := NewScheduler(context.Background())
	g := newGate()
	_, _ = s.Submit(t.Context(), g.job("a", "p", false, testPolicy()))
	g.next(t)
	s.Stop()
	s.Wait(context.Background())
	if _, err := s.Submit(t.Context(), g.job("b", "p", false, testPolicy())); !errors.Is(err, ErrStopped) {
		t.Fatalf("admission after stop: %v", err)
	}
	if len(g.started) != 1 {
		t.Fatalf("started %v", g.started)
	}
}
