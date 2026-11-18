package repochange_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/repochange/repochangetest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

// canonicalTestDir matches how Notify canonicalizes a root, so an observer can
// tell its own root's events from a neighbouring test's.
func canonicalTestDir(t *testing.T, dir string) string {
	t.Helper()
	abs, err := filepath.Abs(dir)
	testutil.FailErr(t, "abs", err)
	return filepath.Clean(abs)
}

const watchBarrier = ".watch-barrier"

// ensureLiveRoot binds a watch on dir and returns once its stream reports a
// write, so a test's own writes are not made while the platform stream is
// still starting.
func ensureLiveRoot(t *testing.T, dir string) {
	t.Helper()
	root := canonicalTestDir(t, dir)
	var seen atomic.Bool
	unbind := repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir == root && ev.Kind == repochange.WorktreeChanged && slices.Contains(ev.Paths, watchBarrier) {
			seen.Store(true)
		}
	})
	defer unbind()
	repochange.EnsureRoot(t.Context(), dir)
	testutil.FailErr(t, "write the watch barrier", os.WriteFile(filepath.Join(dir, watchBarrier), nil, 0o644))
	testutil.WaitFor(t, "the watch reports its barrier write", func() bool {
		repochange.ResetDebouncerForTest(t.Context())
		return seen.Load()
	})
}

func TestWatcher_EmitsWorktreeChanged(t *testing.T) {
	t.Cleanup(func() { repochange.ResetDebouncerForTest(context.Background()) }) // Cleanup follows context cancellation.
	repochangetest.Acquire(t)

	dir := t.TempDir()
	// Observers are process-global, so a test that counts every watcher event
	// also counts its neighbours'. Scope to this root.
	root := canonicalTestDir(t, dir)

	var n atomic.Int32
	var src repochange.Source
	t.Cleanup(repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir != root {
			return
		}
		if ev.Kind == repochange.WorktreeChanged && ev.Source == repochange.SourceWatcher && slices.Contains(ev.Paths, "watched.txt") {
			n.Add(1)
			src = ev.Source
		}
	}))

	ensureLiveRoot(t, dir)
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "watched.txt"), []byte("hi"), 0o644))
	testutil.WaitFor(t, "the watcher reports WorktreeChanged", func() bool {
		repochange.ResetDebouncerForTest(t.Context())
		return n.Load() > 0
	})
	if src != repochange.SourceWatcher {
		t.Fatalf("source=%q", src)
	}
}

func TestWatcherEmitsForFilesNamedLikeDirectories(t *testing.T) {
	t.Cleanup(func() { repochange.ResetDebouncerForTest(context.Background()) }) // Cleanup follows context cancellation.
	repochangetest.Acquire(t)

	dir := t.TempDir()
	root := canonicalTestDir(t, dir)

	var mu sync.Mutex
	seen := map[string]bool{}
	t.Cleanup(repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir != root {
			return
		}
		if ev.Kind != repochange.WorktreeChanged || ev.Source != repochange.SourceWatcher {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, p := range ev.Paths {
			seen[p] = true
		}
	}))

	ensureLiveRoot(t, dir)

	want := []string{"build", "vendor", "target", "out", "coverage", "dist", ".git", ".paintedwolf"}
	for _, name := range want {
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644))
	}

	// Wait for each expected path.
	missingNames := func() []string {
		mu.Lock()
		defer mu.Unlock()
		var missing []string
		for _, name := range want {
			if !seen[name] {
				missing = append(missing, name)
			}
		}
		return missing
	}
	testutil.WaitFor(t, "the watcher reports every source-file write", func() bool {
		repochange.ResetDebouncerForTest(t.Context())
		return len(missingNames()) == 0
	})
}

func TestEnsureRootAcceptsRootWithNestedJunk(t *testing.T) {
	repochangetest.Acquire(t)
	dir := t.TempDir()
	testutil.FailErr(t, "mkdir .git", os.Mkdir(filepath.Join(dir, ".git"), 0o755))
	testutil.FailErr(t, "mkdir node_modules", os.Mkdir(filepath.Join(dir, "node_modules"), 0o755))
	testutil.FailErr(t, "nested", os.WriteFile(filepath.Join(dir, "node_modules", "x"), []byte("1"), 0o644))
	repochange.EnsureRoot(t.Context(), dir)
}

func TestWatcherObservesHiddenAndMetadataChildren(t *testing.T) {
	t.Cleanup(func() { repochange.ResetDebouncerForTest(context.Background()) })
	repochangetest.Acquire(t)
	dir := t.TempDir()
	root := canonicalTestDir(t, dir)
	directories := []repochange.WatchDirectory{}
	for _, name := range []string{".hidden", ".paintedwolf", ".git/objects"} {
		absolute := filepath.Join(dir, name)
		testutil.FailErr(t, "metadata directory", os.MkdirAll(absolute, 0700))
		directories = append(directories, repochange.WatchDirectory{Path: absolute})
	}
	var mu sync.Mutex
	seen := map[string]bool{}
	t.Cleanup(repochange.RegisterObserver(func(_ context.Context, event repochange.Event) {
		if event.ProjectDir != root {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, path := range event.Paths {
			seen[path] = true
		}
	}))
	ensureLiveRoot(t, dir)
	repochange.SeedWatch(t.Context(), dir, directories)
	for _, name := range []string{".hidden/new.txt", ".paintedwolf/new.txt", ".git/objects/new.txt", ".git/config"} {
		testutil.FailErr(t, "write metadata child", os.WriteFile(filepath.Join(dir, name), nil, 0600))
	}
	testutil.WaitFor(t, "the hidden and tool-metadata writes are reported", func() bool {
		repochange.ResetDebouncerForTest(t.Context())
		mu.Lock()
		defer mu.Unlock()
		if seen[".git/objects/new.txt"] || seen[".git/config"] {
			t.Fatal("lazy Git metadata triggered source reconciliation")
		}
		return seen[".hidden/new.txt"] && seen[".paintedwolf/new.txt"]
	})
}
