package progress_test

import (
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/progress"
)

func TestCoalescerMergesWritesInWindow(t *testing.T) {
	var mu sync.Mutex
	var got []progress.FlushPayload
	c := progress.NewCoalescer(time.Hour, func(p progress.FlushPayload) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	})

	c.Record("s1", "## Progress\n", "## Progress\n- [ ] a\n")
	c.Record("s1", "## Progress\n- [ ] a\n", "## Progress\n- [ ] a\n- [ ] b\n")
	c.FlushNow("s1")

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("flush count = %d, want 1", len(got))
	}
	p := got[0]
	if p.Baseline != "## Progress\n" {
		t.Errorf("baseline = %q, want first write's prev", p.Baseline)
	}
	if p.Latest != "## Progress\n- [ ] a\n- [ ] b\n" {
		t.Errorf("latest = %q, want last write's next", p.Latest)
	}
	if p.Seq != 1 {
		t.Errorf("seq = %d, want 1", p.Seq)
	}
}

func TestCoalescerSeqMonotonic(t *testing.T) {
	var seqs []int
	c := progress.NewCoalescer(time.Hour, func(p progress.FlushPayload) {
		seqs = append(seqs, p.Seq)
	})
	c.Record("s1", "a", "b")
	c.FlushNow("s1")
	c.Record("s1", "b", "c")
	c.FlushNow("s1")
	if len(seqs) != 2 || seqs[0] != 1 || seqs[1] != 2 {
		t.Fatalf("seqs = %v, want [1 2]", seqs)
	}
}

func TestCoalescerFlushNowNoWindowIsNoop(t *testing.T) {
	called := false
	c := progress.NewCoalescer(time.Hour, func(progress.FlushPayload) { called = true })
	c.FlushNow("s1")
	if called {
		t.Fatal("flush emitted with no open window")
	}
}

func TestCoalescerCloseFlushesPendingAndDrainsActiveEmission(t *testing.T) {
	entered, finish, flushed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var mu sync.Mutex
	var got []progress.FlushPayload
	c := progress.NewCoalescer(time.Hour, func(p progress.FlushPayload) {
		if p.SessionID == "active" {
			close(entered)
			<-finish
		}
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
		if p.SessionID == "pending" {
			close(flushed)
		}
	})
	c.Record("active", "a", "b")
	activeDone := make(chan struct{})
	go func() { c.FlushNow("active"); close(activeDone) }()
	<-entered
	c.Record("pending", "old", "new")
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	<-flushed
	select {
	case <-closed:
		t.Fatal("Close returned while an emission was still running")
	default:
	}
	c.Record("late", "", "ignored")
	c.FlushNow("late")
	close(finish)
	<-activeDone
	<-closed
	c.Close()
	c.Record("after", "", "ignored")
	c.FlushNow("after")
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("emissions = %+v", got)
	}
	for _, p := range got {
		if p.SessionID == "pending" && (p.Baseline != "old" || p.Latest != "new" || p.Seq != 1) {
			t.Fatalf("pending delivery = %+v", p)
		}
	}
}
