//go:build linux

package repochange

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watchfd"
)

// inotifyRoot binds a real inotify watcher to a fresh root and records the
// root-relative paths the watcher publishes.
type inotifyRoot struct {
	t        *testing.T
	root     string
	registry *WatcherRegistry
	mu       sync.Mutex
	changes  []WorktreeChange
}

func newInotifyRoot(t *testing.T, maxDirs int) *inotifyRoot {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve root", err)
	testutil.FailErr(t, "mkdir .git", os.Mkdir(filepath.Join(root, ".git"), 0o755))
	fixture := &inotifyRoot{t: t, root: root, registry: &WatcherRegistry{byDir: map[string]*worktreeWatcher{}, maxDirs: maxDirs, budget: watchfd.NewBudget(maxDirs)}}
	t.Cleanup(fixture.registry.CloseAll)
	unbind := RegisterObserver(func(_ context.Context, event Event) {
		if event.ProjectDir != root || event.Kind != WorktreeChanged || event.Source != SourceWatcher {
			return
		}
		fixture.mu.Lock()
		fixture.changes = append(fixture.changes, event.Changes...)
		fixture.mu.Unlock()
	})
	t.Cleanup(unbind)
	fixture.registry.Ensure(t.Context(), root)
	return fixture
}

func (f *inotifyRoot) waitFor(path string, kind WorktreeChangeKind) {
	f.t.Helper()
	testutil.WaitFor(f.t, 3*time.Second, func() bool {
		ResetDebouncerForTest(f.t.Context())
		f.mu.Lock()
		defer f.mu.Unlock()
		return slices.ContainsFunc(f.changes, func(change WorktreeChange) bool {
			return change.Path == path && (kind == WorktreeChangeUnknown || change.Kind == kind)
		})
	})
}

func (f *inotifyRoot) seen(prefix string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.ContainsFunc(f.changes, func(change WorktreeChange) bool {
		return change.Path == prefix || len(change.Path) > len(prefix) && change.Path[:len(prefix)+1] == prefix+"/"
	})
}

func TestInotifyAtomicSaveReportsTheReplacedFile(t *testing.T) {
	fixture := newInotifyRoot(t, 64)
	doc := filepath.Join(fixture.root, "doc.txt")
	testutil.FailErr(t, "seed document", os.WriteFile(doc, []byte("v1"), 0o644))
	fixture.waitFor("doc.txt", WorktreeChangeUnknown)
	temp := filepath.Join(fixture.root, ".doc.txt.swp-tmp")
	testutil.FailErr(t, "write replacement", os.WriteFile(temp, []byte("v2"), 0o644))
	testutil.FailErr(t, "rename replacement over document", os.Rename(temp, doc))
	fixture.waitFor(".doc.txt.swp-tmp", WorktreeChangeStructural)
	fixture.mu.Lock()
	fixture.changes = nil
	fixture.mu.Unlock()
	testutil.FailErr(t, "write second replacement", os.WriteFile(temp, []byte("v3"), 0o644))
	testutil.FailErr(t, "rename second replacement", os.Rename(temp, doc))
	fixture.waitFor("doc.txt", WorktreeChangeStructural)
}

func TestInotifyDistinguishesCaseSensitiveNames(t *testing.T) {
	fixture := newInotifyRoot(t, 64)
	for _, name := range []string{"Readme.md", "readme.md", "README.md"} {
		testutil.FailErr(t, "write "+name, os.WriteFile(filepath.Join(fixture.root, name), []byte(name), 0o644))
		fixture.waitFor(name, WorktreeChangeUnknown)
	}
}

func TestInotifyReportsExecutableModeChanges(t *testing.T) {
	fixture := newInotifyRoot(t, 64)
	script := filepath.Join(fixture.root, "run.sh")
	testutil.FailErr(t, "seed script", os.WriteFile(script, []byte("#!/bin/sh\n"), 0o644))
	fixture.waitFor("run.sh", WorktreeChangeUnknown)
	fixture.mu.Lock()
	fixture.changes = nil
	fixture.mu.Unlock()
	testutil.FailErr(t, "make executable", os.Chmod(script, 0o755))
	fixture.waitFor("run.sh", WorktreeChangeStructural)
}

// A symlinked directory is an entry of the root, never a registered subtree:
// writes behind it stay invisible, as they are to the catalog.
func TestInotifyDoesNotFollowSymlinkedDirectories(t *testing.T) {
	fixture := newInotifyRoot(t, 64)
	outside := t.TempDir()
	link := filepath.Join(fixture.root, "linked")
	testutil.FailErr(t, "create directory symlink", os.Symlink(outside, link))
	fixture.waitFor("linked", WorktreeChangeStructural)
	before := fixture.registry.Coverage(fixture.root)
	testutil.FailErr(t, "write behind symlink", os.WriteFile(filepath.Join(outside, "hidden.txt"), []byte("x"), 0o644))
	testutil.FailErr(t, "write beside symlink", os.WriteFile(filepath.Join(fixture.root, "visible.txt"), []byte("x"), 0o644))
	fixture.waitFor("visible.txt", WorktreeChangeUnknown)
	if fixture.seen("linked/hidden.txt") {
		t.Fatal("watcher followed a directory symlink out of the root")
	}
	if fixture.registry.DirWatched(fixture.root, "linked") {
		t.Fatal("symlinked directory holds a watch registration")
	}
	if after := fixture.registry.Coverage(fixture.root); after.Watched != before.Watched {
		t.Fatalf("symlink changed registrations: before=%+v after=%+v", before, after)
	}
}

func TestInotifyCoversALargeSeededTreeCompletely(t *testing.T) {
	const fanout, depth = 40, 3
	fixture := newInotifyRoot(t, watchfd.MaxDirs)
	var directories []WatchDirectory
	var deepest string
	for a := range fanout {
		level := filepath.Join(fixture.root, fmt.Sprintf("a%02d", a))
		for b := range depth {
			level = filepath.Join(level, fmt.Sprintf("b%02d", b))
			testutil.FailErr(t, "mkdir", os.MkdirAll(level, 0o755))
			directories = append(directories, WatchDirectory{Path: level, EntryCount: 1})
			deepest = level
		}
	}
	for _, directory := range directories {
		testutil.FailErr(t, "write leaf", os.WriteFile(filepath.Join(directory.Path, "leaf.txt"), []byte("x"), 0o644))
	}
	fixture.registry.Seed(t.Context(), fixture.root, directories)
	coverage := fixture.registry.Coverage(fixture.root)
	if !coverage.Complete || coverage.Truncated != 0 || coverage.WatchLimit || coverage.Watched != len(directories)+1 {
		t.Fatalf("large tree coverage = %+v, want %d complete registrations", coverage, len(directories)+1)
	}
	testutil.FailErr(t, "write in deepest directory", os.WriteFile(filepath.Join(deepest, "edit.txt"), []byte("x"), 0o644))
	rel, err := filepath.Rel(fixture.root, filepath.Join(deepest, "edit.txt"))
	testutil.FailErr(t, "relative deepest path", err)
	fixture.waitFor(filepath.ToSlash(rel), WorktreeChangeUnknown)
}
