package repochange

import "testing"

func TestWatcherLifetimeKeepsStreamsUntilFinalLease(t *testing.T) {
	root := t.TempDir()
	old, current := AcquireWatcherLifetime(), AcquireWatcherLifetime()
	t.Cleanup(old)
	t.Cleanup(current)
	EnsureRoot(t.Context(), root)
	if !Coverage(root).Watching {
		t.Fatal("fixture did not start physical stream")
	}
	old()
	old()
	if !Coverage(root).Watching {
		t.Fatal("old engine stopped current engine stream")
	}
	current()
	current()
	if Coverage(root).Watching {
		t.Fatal("final engine left physical stream alive")
	}
}
