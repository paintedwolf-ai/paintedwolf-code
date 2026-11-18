package repochange

import (
	"context"
	"testing"
	"testing/synctest"
)

func TestWatcherLifetimeKeepsStreamsUntilFinalLease(t *testing.T) {
	root := t.TempDir()
	old, current := AcquireWatcherLifetime(), AcquireWatcherLifetime()
	t.Cleanup(func() { _ = old(context.Background()) })
	t.Cleanup(func() { _ = current(context.Background()) })
	EnsureRoot(t.Context(), root)
	if !Coverage(root).Watching {
		t.Fatal("fixture did not start physical stream")
	}
	if err := old(t.Context()); err != nil {
		t.Fatalf("release old watcher lease: %v", err)
	}
	if err := old(t.Context()); err != nil {
		t.Fatalf("release old watcher lease: %v", err)
	}
	if !Coverage(root).Watching {
		t.Fatal("old engine stopped current engine stream")
	}
	if err := current(t.Context()); err != nil {
		t.Fatalf("release current watcher lease: %v", err)
	}
	if err := current(t.Context()); err != nil {
		t.Fatalf("release current watcher lease: %v", err)
	}
	if Coverage(root).Watching {
		t.Fatal("final engine left physical stream alive")
	}
}

func TestFinalWatcherLeaseSettlesAcceptedChangesAcrossRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		root := t.TempDir()
		releaseOld := AcquireWatcherLifetime()
		entered, finish := make(chan struct{}), make(chan struct{})
		t.Cleanup(RegisterObserver(func(_ context.Context, event Event) {
			if event.ProjectDir == root {
				close(entered)
				<-finish
			}
		}))
		NotifyWorktreeDebounced(t.Context(), root, []string{"accepted"}, SourceMutation)
		epoch := CurrentEpoch(root)
		settled := make(chan error, 1)
		go func() { settled <- releaseOld(t.Context()) }()
		<-entered
		releaseNew := AcquireWatcherLifetime()
		if CurrentEpoch(root) != epoch {
			t.Fatal("restarting streams reset the process epoch")
		}
		synctest.Wait()
		select {
		case err := <-settled:
			t.Fatalf("old lease returned before delivery completed: %v", err)
		default:
		}
		close(finish)
		if err := <-settled; err != nil {
			t.Fatalf("release retired stream generation: %v", err)
		}
		if err := releaseNew(t.Context()); err != nil {
			t.Fatalf("release new stream generation: %v", err)
		}
	})
}
