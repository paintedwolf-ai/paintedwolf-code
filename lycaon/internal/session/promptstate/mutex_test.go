package promptstate

import (
	"sync"
	"testing"
)

func TestSessionLockReleasesIdleEntry(t *testing.T) {
	m := &State{}
	for _, lock := range []*Lock{m.Prompt.Acquire("sess-1"), m.Submission.Acquire("sess-1")} {
		if refs := lockRefCountAndRelease(lock); refs < 1 {
			t.Fatalf("lock refs = %d, want at least 1", refs)
		}
	}
	if got := m.Prompt.len(); got != 0 {
		t.Fatalf("prompt mutex entries = %d, want 0", got)
	}
	if got := m.Submission.len(); got != 0 {
		t.Fatalf("prompt submission mutex entries = %d, want 0", got)
	}
}

func TestSessionLockIsStableUnderConcurrency(t *testing.T) {
	m := &State{}
	const goroutines = 64
	got := make([]*Lock, goroutines)
	var start, done sync.WaitGroup
	start.Add(1)
	done.Add(goroutines)
	for i := range got {
		go func(idx int) {
			defer done.Done()
			start.Wait()
			got[idx] = m.Prompt.Acquire("sess-1")
		}(i)
	}
	start.Done()
	done.Wait()
	for i, lock := range got {
		if lock.entry != got[0].entry {
			t.Fatalf("caller %d received a different mutex for one session", i)
		}
	}
	for _, lock := range got {
		if refs := lockRefCountAndRelease(lock); refs < 1 {
			t.Fatalf("lock refs = %d, want at least 1", refs)
		}
	}
	if got := m.Prompt.len(); got != 0 {
		t.Fatalf("prompt mutex entries = %d, want 0", got)
	}
}

func lockRefCountAndRelease(lock *Lock) int {
	lock.Lock()
	refs := lock.entry.refs
	lock.Unlock()
	return refs
}
