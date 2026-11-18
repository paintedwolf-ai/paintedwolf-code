package repochange

import (
	"context"
	"sync"
	"testing"
)

func TestNotifyFiresMatchingObserver(t *testing.T) {
	const dir = "/repo-fire"
	var mu sync.Mutex
	var seen []Event
	t.Cleanup(RegisterObserver(func(_ context.Context, ev Event) {
		if ev.ProjectDir != dir {
			return
		}
		mu.Lock()
		seen = append(seen, ev)
		mu.Unlock()
	}))

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
	t.Cleanup(RegisterObserver(func(_ context.Context, ev Event) {
		if ev.ProjectDir == dir {
			fired = true
		}
	}))

	Notify(context.Background(), Event{ProjectDir: dir}) // Kind 0 → dropped
	Notify(context.Background(), Event{Kind: HeadMoved}) // no dir → dropped

	if fired {
		t.Fatal("zero-value events must not reach observers")
	}
}

func TestObserverReleasePreservesOtherObserversAndEpoch(t *testing.T) {
	root := t.TempDir()
	before := CurrentEpoch(root)
	var first, second int
	releaseFirst := RegisterObserver(func(_ context.Context, ev Event) {
		if ev.ProjectDir == root {
			first++
		}
	})
	t.Cleanup(releaseFirst)
	t.Cleanup(RegisterObserver(func(_ context.Context, ev Event) {
		if ev.ProjectDir == root {
			second++
		}
	}))
	Notify(t.Context(), Event{ProjectDir: root, Kind: WorktreeChanged})
	releaseFirst()
	releaseFirst()
	Notify(t.Context(), Event{ProjectDir: root, Kind: WorktreeChanged})
	if first != 1 || second != 2 {
		t.Fatalf("observer deliveries = %d, %d; want 1, 2", first, second)
	}
	after := CurrentEpoch(root)
	if after.BootID != before.BootID || after.Value != before.Value+2 {
		t.Fatalf("epoch changed from %+v to %+v across observer release", before, after)
	}
}
