package repochange

import (
	"context"
	"sync"
	"testing"
)

// Observers are process-global and cannot be unregistered, so each test scopes
// its observer to a unique ProjectDir and ignores everything else.

func TestNotifyFiresMatchingObserver(t *testing.T) {
	const dir = "/repo-fire"
	var mu sync.Mutex
	var seen []Event
	RegisterObserver(func(_ context.Context, ev Event) {
		if ev.ProjectDir != dir {
			return
		}
		mu.Lock()
		seen = append(seen, ev)
		mu.Unlock()
	})

	Notify(context.Background(), Event{ProjectDir: dir, Kind: HeadMoved})

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0].Kind != HeadMoved {
		t.Fatalf("observer saw %#v, want one HeadMoved", seen)
	}
}

func TestNotifyIgnoresZeroValueEvents(t *testing.T) {
	const dir = "/repo-zero"
	fired := false
	RegisterObserver(func(_ context.Context, ev Event) {
		if ev.ProjectDir == dir {
			fired = true
		}
	})

	Notify(context.Background(), Event{ProjectDir: dir}) // Kind 0 → dropped
	Notify(context.Background(), Event{Kind: HeadMoved}) // no dir → dropped

	if fired {
		t.Fatal("zero-value events must not reach observers")
	}
}

func TestRegisterNilObserverIsSafe(t *testing.T) {
	RegisterObserver(nil)
	// Must not panic.
	Notify(context.Background(), Event{ProjectDir: "/repo-nil", Kind: HeadMoved})
}
