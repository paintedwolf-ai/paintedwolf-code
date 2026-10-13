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
	// The staging name is gone by the time its events are classified, so its
	// kind is unknown; the replaced document arrives as a structural change.
	temp := filepath.Join(fixture.root, ".doc.txt.swp-tmp")
	testutil.FailErr(t, "write replacement", os.WriteFile(temp, []byte("v2"), 0o644))
	testutil.FailErr(t, "rename replacement over document", os.Rename(temp, doc))
	fixture.waitFor(".doc.txt.swp-tmp", WorktreeChangeUnknown)
	fixture.mu.Lock()
	fixture.changes = nil
	fixture.mu.Unlock()
	testutil.FailErr(t, "write second replacement", os.WriteFile(temp, []byte("v3"), 0o644))
	testutil.FailErr(t, "rename second replacement", os.Rename(temp, doc))
	fixture.waitFor("doc.txt", WorktreeChangeUnknown)
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if !slices.ContainsFunc(fixture.changes, func(change WorktreeChange) bool {
		return change.Path == "doc.txt" && change.Kind == WorktreeChangeStructural
	}) {
		t.Fatalf("replaced document was not reported structurally: %+v", fixture.changes)
	}
}

// A burst wider than the kernel queue drops events; the stream reports the
// overflow, one resync closes the window, and coverage ends complete. Run with
// a lowered fs.inotify.max_queued_events to exercise the overflow path.
func TestInotifyBurstKeepsCoverageComplete(t *testing.T) {
	const files = 3000
	fixture := newInotifyRoot(t, 64)
	burst := filepath.Join(fixture.root, "burst")
	testutil.FailErr(t, "mkdir burst", os.Mkdir(burst, 0o755))
	fixture.waitFor("burst", WorktreeChangeUnknown)
	for i := range files {
		testutil.FailErr(t, "write burst file", os.WriteFile(filepath.Join(burst, fmt.Sprintf("f%04d", i)), []byte("x"), 0o644))
	}
	// The marker's own event may fall into a dropped window; the resync that
	// closes the window is then what tells observers to look again.
	testutil.FailErr(t, "write marker", os.WriteFile(filepath.Join(fixture.root, "after-burst.txt"), []byte("x"), 0o644))
	testutil.WaitFor(t, 3*time.Second, func() bool {
		ResetDebouncerForTest(t.Context())
		return fixture.seen("after-burst.txt") || fixture.seen(".")
	})
	testutil.WaitFor(t, 3*time.Second, func() bool { return fixture.registry.Coverage(fixture.root).Complete })
	coverage := fixture.registry.Coverage(fixture.root)
	t.Logf("burst of %d writes: overflows=%d watched=%d complete=%t marker=%t resync=%t", files, coverage.Overflows, coverage.Watched, coverage.Complete, fixture.seen("after-burst.txt"), fixture.seen("."))
	if coverage.Faulted || coverage.Truncated != 0 {
		t.Fatalf("burst coverage = %+v", coverage)
	}
	if (coverage.Overflows > 0 && !fixture.seen(".")) || (coverage.Overflows == 0 && !fixture.seen("after-burst.txt")) {
		t.Fatal("neither the marker nor an overflow resync reached observers")
	}
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
	// The root and the repository's ref surfaces register beside the seeded tree.
	coverage := fixture.registry.Coverage(fixture.root)
	if !coverage.Complete || coverage.Truncated != 0 || coverage.WatchLimit || coverage.Watched < len(directories)+1 {
		t.Fatalf("large tree coverage = %+v, want at least %d complete registrations", coverage, len(directories)+1)
	}
	testutil.FailErr(t, "write in deepest directory", os.WriteFile(filepath.Join(deepest, "edit.txt"), []byte("x"), 0o644))
	rel, err := filepath.Rel(fixture.root, filepath.Join(deepest, "edit.txt"))
	testutil.FailErr(t, "relative deepest path", err)
	fixture.waitFor(filepath.ToSlash(rel), WorktreeChangeUnknown)
}
