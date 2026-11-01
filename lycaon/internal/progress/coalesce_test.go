package progress_test

import (
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/progress"
)

func testSnap(content string, ver uint64, ids ...string) progress.Snapshot {
	return progress.NewSnapshot("", content, ids...).WithDocumentVersion(ver)
}

func TestCoalescerMergesWritesInWindow(t *testing.T) {
	var mu sync.Mutex
	var got []progress.FlushPayload
	c := progress.NewCoalescer(time.Hour, func(p progress.FlushPayload) {
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
	})

	c.Record("s1", testSnap("## Progress\n", 0), testSnap("## Progress\n- [ ] a\n", 1, "id-a"))
	c.Record("s1", testSnap("## Progress\n- [ ] a\n", 1, "id-a"), testSnap("## Progress\n- [ ] a\n- [ ] b\n", 2, "id-a", "id-b"))
	c.FlushNow("s1")

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("flush count = %d, want 1", len(got))
	}
	p := got[0]
	if p.Baseline.Content != "## Progress" {
		t.Errorf("baseline = %q, want first write's prev", p.Baseline.Content)
	}
	if p.Latest.Content != "## Progress\n- [ ] a\n- [ ] b" {
		t.Errorf("latest = %q, want last write's next", p.Latest.Content)
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
	c.Record("s1", testSnap("a", 0), testSnap("b", 1))
	c.FlushNow("s1")
	c.Record("s1", testSnap("b", 1), testSnap("c", 2))
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
	c.Record("active", testSnap("a", 0), testSnap("b", 1))
	activeDone := make(chan struct{})
	go func() { c.FlushNow("active"); close(activeDone) }()
	<-entered
	c.Record("pending", testSnap("old", 0), testSnap("new", 1))
	closed := make(chan struct{})
	go func() { c.Close(); close(closed) }()
	<-flushed
	select {
	case <-closed:
		t.Fatal("Close returned while an emission was still running")
	default:
	}
	c.Record("late", testSnap("", 0), testSnap("ignored", 1))
	c.FlushNow("late")
	close(finish)
	<-activeDone
	<-closed
	c.Close()
	c.Record("after", testSnap("", 0), testSnap("ignored", 1))
	c.FlushNow("after")
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("emissions = %+v", got)
	}
	for _, p := range got {
		if p.SessionID == "pending" && (p.Baseline.Content != "old" || p.Latest.Content != "new" || p.Seq != 1) {
			t.Fatalf("pending delivery = %+v", p)
		}
	}
}

func TestCoalescerIgnoresStaleDocumentVersionsAndSerializesSessionFlushes(t *testing.T) {
	firstEntered := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondDone := make(chan struct{})
	var mu sync.Mutex
	var got []progress.FlushPayload
	var concurrent int
	var maxConcurrent int

	c := progress.NewCoalescer(time.Hour, func(p progress.FlushPayload) {
		mu.Lock()
		concurrent++
		if concurrent > maxConcurrent {
			maxConcurrent = concurrent
		}
		mu.Unlock()

		if p.Seq == 1 {
			close(firstEntered)
			<-releaseFirst
		}

		mu.Lock()
		got = append(got, p)
		concurrent--
		mu.Unlock()

		if p.Seq == 2 {
			close(secondDone)
		}
	})

	// Out-of-order notifications within a window keep the earliest baseline and highest version.
	s0 := testSnap("- [ ] a", 1, "id-1")
	s1 := testSnap("- [ ] a\n- [ ] b", 2, "id-1", "id-2")
	s2 := testSnap("- [x] a\n- [ ] b", 3, "id-1", "id-2")
	c.Record("s1", s1, s2)
	c.Record("s1", s0, s1) // stale notification arriving late must not overwrite Latest s2, but updates baseline to s0.
	go c.FlushNow("s1")
	<-firstEntered

	// While Seq 1 is emitting, a stale write (version <= 3) is ignored, while version 4 queues behind Seq 1.
	c.Record("s1", s0, s1)
	s3 := testSnap("- [x] a\n- [x] b", 4, "id-1", "id-2")
	c.Record("s1", s2, s3)
	FlushDone := make(chan struct{})
	go func() {
		c.FlushNow("s1")
		close(FlushDone)
	}()

	close(releaseFirst)
	<-secondDone
	<-FlushDone

	mu.Lock()
	defer mu.Unlock()
	if maxConcurrent != 1 {
		t.Fatalf("max concurrent emissions for same session = %d, want 1", maxConcurrent)
	}
	if len(got) != 2 {
		t.Fatalf("got %d emissions, want 2: %+v", len(got), got)
	}
	if got[0].Seq != 1 || got[0].Baseline.Content != s0.Content || got[0].Latest.Content != s2.Content {
		t.Fatalf("first emission = %+v", got[0])
	}
	if got[1].Seq != 2 || got[1].Baseline.Content != s2.Content || got[1].Latest.Content != s3.Content {
		t.Fatalf("second emission = %+v", got[1])
	}
}
