package repochange_test

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

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

func TestWatcher_EmitsWorktreeChanged(t *testing.T) {
	t.Cleanup(repochange.ResetObserversForTest)
	t.Cleanup(func() { repochange.ResetDebouncerForTest(context.Background()) }) // Cleanup follows context cancellation.
	t.Cleanup(repochange.ResetWatchersForTest)

	dir := t.TempDir()
	// Observers are process-global, so a test that counts every watcher event
	// also counts its neighbours'. Scope to this root.
	root := canonicalTestDir(t, dir)

	var n atomic.Int32
	var src repochange.Source
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
		if ev.ProjectDir != root {
			return
		}
		if ev.Kind == repochange.WorktreeChanged && ev.Source == repochange.SourceWatcher {
			n.Add(1)
			src = ev.Source
		}
	})

	repochange.EnsureRoot(t.Context(), dir)
	time.Sleep(50 * time.Millisecond)
	testutil.FailErr(t, "write file", os.WriteFile(filepath.Join(dir, "watched.txt"), []byte("hi"), 0o644))

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		repochange.ResetDebouncerForTest(t.Context())
		if n.Load() > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n.Load() == 0 {
		t.Fatal("expected watcher WorktreeChanged")
	}
	if src != repochange.SourceWatcher {
		t.Fatalf("source=%q", src)
	}
}

func TestWatcherEmitsForFilesNamedLikeDirectories(t *testing.T) {
	t.Cleanup(repochange.ResetObserversForTest)
	t.Cleanup(func() { repochange.ResetDebouncerForTest(context.Background()) }) // Cleanup follows context cancellation.
	t.Cleanup(repochange.ResetWatchersForTest)

	dir := t.TempDir()
	root := canonicalTestDir(t, dir)

	var mu sync.Mutex
	seen := map[string]bool{}
	repochange.RegisterObserver(func(_ context.Context, ev repochange.Event) {
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
	})

	repochange.EnsureRoot(t.Context(), dir)
	time.Sleep(50 * time.Millisecond)

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
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		repochange.ResetDebouncerForTest(t.Context())
		if len(missingNames()) == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if missing := missingNames(); len(missing) > 0 {
		t.Fatalf("watcher dropped source-file writes: %v", missing)
	}
}

func TestEnsureRootAcceptsRootWithNestedJunk(t *testing.T) {
	t.Cleanup(repochange.ResetWatchersForTest)
	dir := t.TempDir()
	testutil.FailErr(t, "mkdir .git", os.Mkdir(filepath.Join(dir, ".git"), 0o755))
	testutil.FailErr(t, "mkdir node_modules", os.Mkdir(filepath.Join(dir, "node_modules"), 0o755))
	testutil.FailErr(t, "nested", os.WriteFile(filepath.Join(dir, "node_modules", "x"), []byte("1"), 0o644))
	repochange.EnsureRoot(t.Context(), dir)
}

func TestWatcherObservesHiddenAndMetadataChildren(t *testing.T) {
	t.Cleanup(repochange.ResetObserversForTest)
	t.Cleanup(func() { repochange.ResetDebouncerForTest(context.Background()) })
	t.Cleanup(repochange.ResetWatchersForTest)
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
	repochange.RegisterObserver(func(_ context.Context, event repochange.Event) {
		if event.ProjectDir != root {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		for _, path := range event.Paths {
			seen[path] = true
		}
	})
	repochange.EnsureRoot(t.Context(), dir)
	repochange.SeedWatch(t.Context(), dir, directories)
	time.Sleep(50 * time.Millisecond)
	for _, name := range []string{".hidden/new.txt", ".paintedwolf/new.txt", ".git/objects/new.txt", ".git/config"} {
		testutil.FailErr(t, "write metadata child", os.WriteFile(filepath.Join(dir, name), nil, 0600))
	}
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		repochange.ResetDebouncerForTest(t.Context())
		mu.Lock()
		complete := seen[".hidden/new.txt"] && seen[".paintedwolf/new.txt"]
		lazyMetadata := seen[".git/objects/new.txt"] || seen[".git/config"]
		mu.Unlock()
		if lazyMetadata {
			t.Fatal("lazy Git metadata triggered source reconciliation")
		}
		if complete {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	t.Fatalf("missing metadata events: %+v", seen)
}
