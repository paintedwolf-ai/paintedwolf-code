package repochange

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/fsnotify/fsnotify"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSeedExistingDoesNotResurrectClosedRootWatcher(t *testing.T) {
	reg := newWatcherRegistry()
	t.Cleanup(reg.CloseAll)

	surviving, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve surviving root", err)
	detached, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve detached root", err)
	detachedSub := filepath.Join(detached, "sub")
	testutil.FailErr(t, "mkdir detached sub", os.MkdirAll(detachedSub, 0o755))

	var mu sync.Mutex
	var survivingChanges []string
	unbind := RegisterObserver(func(_ context.Context, event Event) {
		if event.ProjectDir == surviving && event.Kind == WorktreeChanged && event.Source == SourceWatcher {
			mu.Lock()
			survivingChanges = append(survivingChanges, event.Paths...)
			mu.Unlock()
		}
	})
	t.Cleanup(unbind)

	reg.Ensure(t.Context(), surviving)
	reg.Ensure(t.Context(), detached)
	reg.SeedExisting(t.Context(), detached, []WatchDirectory{{Path: detached}, {Path: detachedSub}})
	testutil.WaitFor(t, "both roots complete coverage", func() bool {
		return reg.Coverage(surviving).Complete && reg.Coverage(detached).Complete
	})
	if !reg.DirWatched(detached, "sub") {
		t.Fatal("detached subdirectory was not watched before CloseRoot")
	}

	reg.CloseRoot(detached)
	if cov := reg.Coverage(detached); cov.Watching {
		t.Fatalf("detached root still watched after CloseRoot: %+v", cov)
	}
	if reg.DirWatched(detached, "sub") {
		t.Fatal("detached subdirectory still watched after CloseRoot")
	}

	// Delayed seeding after CloseRoot must not recreate the retired watcher.
	reg.SeedExisting(t.Context(), detached, []WatchDirectory{{Path: detached}, {Path: detachedSub}})
	if cov := reg.Coverage(detached); cov.Watching {
		t.Fatalf("SeedExisting resurrected closed root watcher: %+v", cov)
	}
	if reg.DirWatched(detached, "sub") {
		t.Fatal("SeedExisting resurrected detached subdirectory watch")
	}

	// Surviving root retains coverage and continues delivering live writes.
	if cov := reg.Coverage(surviving); !cov.Watching || !cov.Complete {
		t.Fatalf("surviving root coverage disrupted: %+v", cov)
	}
	testutil.FailErr(t, "write surviving file", os.WriteFile(filepath.Join(surviving, "alive.txt"), []byte("ok"), 0o644))
	testutil.WaitFor(t, "surviving root delivers write", func() bool {
		ResetDebouncerForTest(t.Context())
		mu.Lock()
		defer mu.Unlock()
		return slices.Contains(survivingChanges, "alive.txt")
	})

	// Explicit future reattachment succeeds cleanly.
	reg.Ensure(t.Context(), detached)
	testutil.WaitFor(t, "reattached root completes coverage", func() bool {
		return reg.Coverage(detached).Complete
	})
}

func TestWatcherTransitionsToFaultedOnRootOutageAndRecoversOnRebind(t *testing.T) {
	t.Run("missing root directory", func(t *testing.T) {
		reg := newWatcherRegistry()
		t.Cleanup(reg.CloseAll)
		parent, err := filepath.EvalSymlinks(t.TempDir())
		testutil.FailErr(t, "resolve parent", err)
		root := filepath.Join(parent, "mounted")
		testutil.FailErr(t, "mkdir root", os.MkdirAll(root, 0o755))

		var mu sync.Mutex
		var paths []string
		var resyncs atomic.Int32
		unbind := RegisterObserver(func(_ context.Context, event Event) {
			if event.ProjectDir != root || event.Kind != WorktreeChanged || event.Source != SourceWatcher {
				return
			}
			mu.Lock()
			paths = append(paths, event.Paths...)
			mu.Unlock()
			if slices.Contains(event.Paths, ".") {
				resyncs.Add(1)
			}
		})
		t.Cleanup(unbind)

		reg.Ensure(t.Context(), root)
		testutil.WaitFor(t, "initial coverage complete", func() bool {
			return reg.Coverage(root).Complete
		})
		resyncsBefore := resyncs.Load()

		// Simulate volume unmount where the root path disappears.
		testutil.FailErr(t, "remove mounted root", os.RemoveAll(root))
		cov := reg.Coverage(root)
		if !cov.Watching || !cov.Faulted || cov.Complete {
			t.Fatalf("coverage during missing-root outage = %+v, want Faulted=true Complete=false", cov)
		}
		testutil.WaitFor(t, "outage emits resync", func() bool {
			ResetDebouncerForTest(t.Context())
			return resyncs.Load() > resyncsBefore
		})

		// Ensure while still unavailable keeps the root faulted.
		reg.Ensure(t.Context(), root)
		if cov := reg.Coverage(root); !cov.Faulted || cov.Complete {
			t.Fatalf("coverage during continued outage = %+v, want Faulted=true", cov)
		}

		// Simulate remount and rebind via Ensure.
		testutil.FailErr(t, "recreate remounted root", os.MkdirAll(root, 0o755))
		reg.Ensure(t.Context(), root)
		testutil.WaitFor(t, "remounted coverage complete", func() bool {
			cov := reg.Coverage(root)
			return cov.Watching && !cov.Faulted && cov.Complete
		})

		mu.Lock()
		paths = nil
		mu.Unlock()
		testutil.FailErr(t, "write after remount", os.WriteFile(filepath.Join(root, "recovered.txt"), []byte("after"), 0o644))
		testutil.WaitFor(t, "post-remount write observed", func() bool {
			ResetDebouncerForTest(t.Context())
			mu.Lock()
			defer mu.Unlock()
			return slices.Contains(paths, "recovered.txt")
		})
	})

	t.Run("replaced mountpoint directory identity", func(t *testing.T) {
		reg := newWatcherRegistry()
		t.Cleanup(reg.CloseAll)
		parent, err := filepath.EvalSymlinks(t.TempDir())
		testutil.FailErr(t, "resolve parent", err)
		root := filepath.Join(parent, "mountpoint")
		testutil.FailErr(t, "mkdir mountpoint", os.MkdirAll(root, 0o755))

		var mu sync.Mutex
		var paths []string
		unbind := RegisterObserver(func(_ context.Context, event Event) {
			if event.ProjectDir == root && event.Kind == WorktreeChanged && event.Source == SourceWatcher {
				mu.Lock()
				paths = append(paths, event.Paths...)
				mu.Unlock()
			}
		})
		t.Cleanup(unbind)

		reg.Ensure(t.Context(), root)
		testutil.WaitFor(t, "initial coverage complete", func() bool {
			return reg.Coverage(root).Complete
		})

		// Replace root with a different directory inode (models unmounting a
		// mountpoint and exposing the underlying empty mountpoint directory).
		testutil.FailErr(t, "move original mount aside", os.Rename(root, filepath.Join(parent, "unmounted-1")))
		testutil.FailErr(t, "create empty mountpoint", os.MkdirAll(root, 0o755))

		cov := reg.Coverage(root)
		if !cov.Watching || !cov.Faulted || cov.Complete {
			t.Fatalf("coverage during mountpoint outage = %+v, want Faulted=true Complete=false", cov)
		}
		// Ensure while still looking at the unmounted mountpoint stays faulted.
		reg.Ensure(t.Context(), root)
		if cov := reg.Coverage(root); !cov.Faulted || cov.Complete {
			t.Fatalf("coverage while still unmounted = %+v, want Faulted=true", cov)
		}

		// Replace again with a fresh directory identity (models remount).
		testutil.FailErr(t, "move empty mountpoint aside", os.Rename(root, filepath.Join(parent, "unmounted-2")))
		testutil.FailErr(t, "create remounted volume", os.MkdirAll(root, 0o755))

		reg.Ensure(t.Context(), root)
		testutil.WaitFor(t, "remounted mountpoint coverage complete", func() bool {
			cov := reg.Coverage(root)
			return cov.Watching && !cov.Faulted && cov.Complete
		})

		mu.Lock()
		paths = nil
		mu.Unlock()
		testutil.FailErr(t, "write after mountpoint remount", os.WriteFile(filepath.Join(root, "post-remount.txt"), []byte("live"), 0o644))
		testutil.WaitFor(t, "post-remount mountpoint write observed", func() bool {
			ResetDebouncerForTest(t.Context())
			mu.Lock()
			defer mu.Unlock()
			return slices.Contains(paths, "post-remount.txt")
		})
	})

	t.Run("kernel dropped watch without stream event", func(t *testing.T) {
		root := t.TempDir()
		info, err := os.Stat(root)
		testutil.FailErr(t, "stat root", err)
		platform := &errorTestWatcher{events: make(chan fsnotify.Event), errors: make(chan error, 1), resync: make(chan struct{})}
		w := &worktreeWatcher{root: root, watcher: platform, done: make(chan struct{}), watched: map[string]int{root: 1}, rootInfo: info}
		if cov := w.coverage(); !cov.Complete || cov.Faulted {
			t.Fatalf("initial coverage = %+v, want Complete=true Faulted=false", cov)
		}
		var resynced atomic.Bool
		unbind := RegisterObserver(func(_ context.Context, event Event) {
			if event.ProjectDir == root && event.Kind == WorktreeChanged && slices.Contains(event.Paths, ".") {
				resynced.Store(true)
			}
		})
		defer unbind()

		// Simulate kernel watch loss on a still-current live root.
		platform.watchOff.Store(true)
		cov := w.coverage()
		if !cov.Faulted || cov.Complete {
			t.Fatalf("coverage after silent watch loss = %+v, want Faulted=true Complete=false", cov)
		}
		if !resynced.Load() {
			t.Fatal("silent watch loss did not emit a resync event")
		}
		// Because the live root identity did not change into an unmounted
		// mountpoint, needsRebind must immediately allow watch reinstallation.
		if !w.needsRebind() {
			t.Fatal("still-current root with dropped watch remained stuck in needsRebind=false")
		}
	})

	t.Run("closed watcher suppresses late health notifications", func(t *testing.T) {
		root := t.TempDir()
		info, err := os.Stat(root)
		testutil.FailErr(t, "stat root", err)
		platform := &errorTestWatcher{events: make(chan fsnotify.Event), errors: make(chan error, 1), resync: make(chan struct{})}
		w := &worktreeWatcher{root: root, watcher: platform, done: make(chan struct{}), watched: map[string]int{root: 1}, rootInfo: info}
		var resyncs atomic.Int32
		unbind := RegisterObserver(func(_ context.Context, event Event) {
			if event.ProjectDir == root && event.Kind == WorktreeChanged && slices.Contains(event.Paths, ".") {
				resyncs.Add(1)
			}
		})
		defer unbind()

		w.close()
		platform.watchOff.Store(true)
		if w.verifyRootHealth() {
			t.Fatal("closed watcher reported healthy")
		}
		if w.needsRebind() {
			t.Fatal("closed watcher requested rebind")
		}
		if got := resyncs.Load(); got != 0 {
			t.Fatalf("closed watcher emitted %d late resync notifications, want 0", got)
		}
	})
}

type barrierCloseWatcher struct {
	errorTestWatcher
	entered chan struct{}
	release chan struct{}
}

func (b *barrierCloseWatcher) Close() error {
	close(b.entered)
	<-b.release
	return nil
}

func TestEnsureReplacementAbortsWhenCloseRootIntervenes(t *testing.T) {
	reg := newWatcherRegistry()
	t.Cleanup(reg.CloseAll)
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve root", err)
	info, err := os.Stat(root)
	testutil.FailErr(t, "stat root", err)

	barrier := &barrierCloseWatcher{
		errorTestWatcher: errorTestWatcher{events: make(chan fsnotify.Event), errors: make(chan error, 1), resync: make(chan struct{})},
		entered:          make(chan struct{}),
		release:          make(chan struct{}),
	}
	barrier.watchOff.Store(true) // triggers needsRebind() == true on Ensure
	existing := &worktreeWatcher{
		root:     root,
		watcher:  barrier,
		done:     make(chan struct{}),
		watched:  map[string]int{root: 1},
		rootInfo: info,
	}
	reg.mu.Lock()
	reg.byDir[root] = existing
	reg.mu.Unlock()

	ensureDone := make(chan struct{})
	go func() {
		defer close(ensureDone)
		reg.Ensure(t.Context(), root)
	}()

	// Wait until Ensure has removed existing and paused inside existing.close().
	testutil.AwaitClosed(t, "Ensure enters close barrier", barrier.entered)

	// Concurrent CloseRoot retires the root while Ensure is mid-replacement.
	reg.CloseRoot(root)
	close(barrier.release)
	testutil.AwaitClosed(t, "Ensure finishes after CloseRoot", ensureDone)

	if cov := reg.Coverage(root); cov.Watching {
		t.Fatalf("stale Ensure resurrected retired root after CloseRoot: %+v", cov)
	}

	// Subsequent valid reattachment succeeds cleanly.
	reg.Ensure(t.Context(), root)
	testutil.WaitFor(t, "reattached root completes coverage", func() bool {
		return reg.Coverage(root).Complete
	})
}

func TestWatcherRegistryChurnLeavesNoRetainedRetirementState(t *testing.T) {
	reg := newWatcherRegistry()
	t.Cleanup(reg.CloseAll)
	base, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve base", err)
	for i := range 200 {
		dir := filepath.Join(base, filepath.FromSlash("root"), string(rune('a'+i%26))+filepath.Base(t.TempDir()))
		testutil.FailErr(t, "mkdir churn dir", os.MkdirAll(dir, 0o755))
		reg.Ensure(t.Context(), dir)
		reg.CloseRoot(dir)
	}
	reg.mu.Lock()
	activeDirs := len(reg.byDir)
	activeFences := len(reg.replacing)
	reg.mu.Unlock()
	if activeDirs != 0 || activeFences != 0 {
		t.Fatalf("retained registry state after churn: byDir=%d replacing=%d, want 0/0", activeDirs, activeFences)
	}
}
