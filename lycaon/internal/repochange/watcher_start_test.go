package repochange

import (
	"context"
	"sync"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWatchStartResyncPrecedesCompleteCoverage(t *testing.T) {
	root := t.TempDir()
	before := CurrentEpoch(root)
	registry := newWatcherRegistry()
	t.Cleanup(registry.CloseAll)
	entered := make(chan Event, 2)
	finish := make(chan struct{})
	release := sync.OnceFunc(func() { close(finish) })
	t.Cleanup(release)
	t.Cleanup(RegisterObserver(func(_ context.Context, event Event) {
		if event.ProjectDir == root && event.Source == SourceWatcher {
			entered <- event
			<-finish
		}
	}))
	registry.Ensure(t.Context(), root)
	event := testutil.Receive(t, "initial root resync", entered)
	if !StructuralPaths(event).Full || event.WorktreeEpoch <= before.Value {
		t.Fatalf("initial event = %+v, want a new full-root observation", event)
	}
	if registry.Coverage(root).Complete {
		t.Fatal("coverage became complete before resync delivery finished")
	}
	release()
	testutil.WaitFor(t, "initial coverage is complete", func() bool { return registry.Coverage(root).Complete })
	registry.CloseRoot(root)
	before = CurrentEpoch(root)
	registry.Ensure(t.Context(), root)
	event = testutil.Receive(t, "reopened root resync", entered)
	if !StructuralPaths(event).Full || event.WorktreeEpoch <= before.Value {
		t.Fatalf("reopened event = %+v, want a new full-root observation", event)
	}
}
