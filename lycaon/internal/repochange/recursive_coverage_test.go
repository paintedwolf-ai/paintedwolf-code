package repochange

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watchfd"
)

// skipUnlessRecursive skips a test whose claim only holds where one root
// registration observes the whole tree.
func skipUnlessRecursive(t *testing.T) {
	t.Helper()
	if !watchfd.Recursive {
		t.Skip("this backend registers one directory at a time")
	}
}

// A directory cap and a spent budget describe kqueue's shortfall, not a
// recursive stream's. Coverage is complete however large the tree.
func TestRecursiveWatchIsCompleteBeyondTheDirectoryCap(t *testing.T) {
	skipUnlessRecursive(t)
	const dirs = 12
	root := fanoutWorktree(t, dirs, 2)

	reg := &WatcherRegistry{
		byDir:   map[string]*worktreeWatcher{},
		maxDirs: 2,
		budget:  watchfd.NewBudget(0),
	}
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)
	reg.Seed(t.Context(), root, fanoutWatchDirectories(root, dirs, 2))

	coverage := reg.Coverage(root)
	if !coverage.Watching || !coverage.Complete || coverage.Truncated != 0 || !coverage.Recursive {
		t.Fatalf("coverage = %+v, want a complete recursive watch", coverage)
	}
	if spent := reg.spentForTest(); spent != 0 {
		t.Fatalf("recursive watch spent %d descriptors, want 0", spent)
	}
	for d := range dirs {
		dir := fmt.Sprintf("d%03d", d)
		if !reg.DirWatched(root, dir) {
			t.Fatalf("%s lies under a recursive root stream but DirWatched reported no watch", dir)
		}
	}
}

// Recursive coverage follows the eager plane within the root.
func TestRecursiveDirWatchedFollowsTheRootGeometry(t *testing.T) {
	skipUnlessRecursive(t)
	root := t.TempDir()
	testutil.FailErr(t, "mkdir .git", os.Mkdir(filepath.Join(root, ".git"), 0o755))
	deep := filepath.Join(root, "a", "b", "c")
	testutil.FailErr(t, "mkdir deep", os.MkdirAll(deep, 0o755))

	reg := newBudgetedRegistry(0)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)

	for _, dir := range []string{".", "a", "a/b/c", "a/b/c/never-listed"} {
		if !reg.DirWatched(root, dir) {
			t.Fatalf("DirWatched(%q) = false under a recursive root stream", dir)
		}
	}
	for _, dir := range []string{"..", "../sibling", ".git", ".git/refs"} {
		if reg.DirWatched(root, dir) {
			t.Fatalf("DirWatched(%q) = true, want the outside or policy boundary reported unwatched", dir)
		}
	}
}

// A write in a directory nobody seeded still moves the epoch, which is the
// whole reason coverage may call itself complete without registrations.
func TestRecursiveWatchObservesWritesInUnseededDirectories(t *testing.T) {
	skipUnlessRecursive(t)
	t.Cleanup(ResetObserversForTest)
	t.Cleanup(func() { ResetDebouncerForTest(context.Background()) })
	root := t.TempDir()
	deep := filepath.Join(root, "src", "generated", "nested")
	testutil.FailErr(t, "mkdir deep", os.MkdirAll(deep, 0o755))

	var seen atomic.Bool
	unregister := RegisterObserver(func(_ context.Context, ev Event) {
		if ev.ProjectDir != root || ev.Kind != WorktreeChanged || ev.Source != SourceWatcher {
			return
		}
		for _, p := range ev.Paths {
			if p == "src/generated/nested/artifact.bin" {
				seen.Store(true)
			}
		}
	})
	t.Cleanup(unregister)

	reg := newBudgetedRegistry(0)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)
	time.Sleep(50 * time.Millisecond)
	testutil.FailErr(t, "write artifact",
		os.WriteFile(filepath.Join(deep, "artifact.bin"), []byte("x"), 0o644))

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !seen.Load() {
		ResetDebouncerForTest(t.Context())
		time.Sleep(20 * time.Millisecond)
	}
	if !seen.Load() {
		t.Fatal("a write three directories below an unseeded root never reached the epoch")
	}
	// The event's directory was never registered; under a recursive stream
	// that is not a shortfall and must not be counted as one.
	if coverage := reg.Coverage(root); !coverage.Complete || coverage.Truncated != 0 {
		t.Fatalf("coverage = %+v after a write in an unregistered directory, want it still complete", coverage)
	}
}
