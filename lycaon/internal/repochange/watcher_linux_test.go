//go:build linux

package repochange

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watchfd"
)

// inotifyRoot binds a real inotify watcher to a fresh root and records the
// root-relative paths the watcher publishes.
type inotifyRoot struct {
	t        *testing.T
	root     string
	registry *watcherRegistry
	mu       sync.Mutex
	changes  []WorktreeChange
}

func newInotifyRoot(t *testing.T, maxDirs int) *inotifyRoot {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve root", err)
	testutil.FailErr(t, "mkdir .git", os.Mkdir(filepath.Join(root, ".git"), 0o755))
	fixture := &inotifyRoot{t: t, root: root, registry: &watcherRegistry{byDir: map[string]*worktreeWatcher{}, maxDirs: maxDirs, budget: watchfd.NewBudget(maxDirs)}}
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
	testutil.WaitFor(f.t, "the change is reported", func() bool {
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
	testutil.WaitFor(t, "the post-burst change is reported", func() bool {
		ResetDebouncerForTest(t.Context())
		return fixture.seen("after-burst.txt") || fixture.seen(".")
	})
	testutil.WaitFor(t, "coverage is complete", func() bool { return fixture.registry.Coverage(fixture.root).Complete })
	coverage := fixture.registry.Coverage(fixture.root)
	t.Logf("burst of %d writes: overflows=%d watched=%d complete=%t marker=%t resync=%t", files, coverage.Overflows, coverage.Watched, coverage.Complete, fixture.seen("after-burst.txt"), fixture.seen("."))
	if coverage.Faulted || coverage.Truncated != 0 {
		t.Fatalf("burst coverage = %+v", coverage)
	}
	// A root resync covers the marker whatever produced it: a kernel overflow
	// window, or the debouncer collapsing a burst wider than maxDebouncePaths
	// into the whole tree, which absorbs the marker's own entry.
	if !fixture.seen("after-burst.txt") && !fixture.seen(".") {
		t.Fatal("neither the marker nor a root resync reached observers")
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
	testutil.WaitFor(t, "seeded watch coverage is complete", func() bool { return fixture.registry.Coverage(fixture.root).Complete })
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

// inotifyInstances counts the inotify instances this process holds.
func inotifyInstances(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	testutil.FailErr(t, "list descriptors", err)
	count := 0
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err == nil && target == "anon_inode:inotify" {
			count++
		}
	}
	return count
}

// Instances are capped per user across every process (max_user_instances is
// commonly 128), so roots share one stream however many the engine watches.
func TestInotifyRootsShareOneInstance(t *testing.T) {
	const roots = 16
	fixtures := make([]*inotifyRoot, 0, roots)
	for range roots {
		fixtures = append(fixtures, newInotifyRoot(t, 64))
	}
	if got := inotifyInstances(t); got != 1 {
		t.Fatalf("%d watched roots hold %d inotify instances, want 1", roots, got)
	}
	for _, fixture := range fixtures {
		testutil.FailErr(t, "write", os.WriteFile(filepath.Join(fixture.root, "edit.txt"), []byte("x"), 0o644))
	}
	for _, fixture := range fixtures {
		fixture.waitFor("edit.txt", WorktreeChangeUnknown)
		if coverage := fixture.registry.Coverage(fixture.root); !coverage.Watching || !coverage.Complete {
			t.Fatalf("root coverage = %+v", coverage)
		}
	}
}

func inotifyWatchedInodes(t *testing.T) map[uint64]bool {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	testutil.FailErr(t, "list descriptors", err)
	inodes := map[uint64]bool{}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
		if err != nil || target != "anon_inode:inotify" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc/self/fdinfo", entry.Name()))
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if !strings.HasPrefix(line, "inotify ") {
				continue
			}
			for _, field := range strings.Fields(line) {
				if hexIno, ok := strings.CutPrefix(field, "ino:"); ok {
					if ino, parseErr := strconv.ParseUint(hexIno, 16, 64); parseErr == nil {
						inodes[ino] = true
					}
				}
			}
		}
	}
	return inodes
}

func dirInode(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	testutil.FailErr(t, "stat "+path, err)
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatalf("unexpected Stat_t for %s", path)
	}
	return stat.Ino
}

func TestInotifyCloseRootRemovesDetachedInodesAndKeepsSurvivor(t *testing.T) {
	surviving := newInotifyRoot(t, 64)
	detachedRoot, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve detached root", err)
	detachedSub := filepath.Join(detachedRoot, "child")
	testutil.FailErr(t, "mkdir detached child", os.MkdirAll(detachedSub, 0o755))

	surviving.registry.Ensure(t.Context(), detachedRoot)
	surviving.registry.SeedExisting(t.Context(), detachedRoot, []WatchDirectory{
		{Path: detachedRoot, EntryCount: 1},
		{Path: detachedSub, EntryCount: 0},
	})
	testutil.WaitFor(t, "detached root seeded", func() bool {
		return surviving.registry.Coverage(detachedRoot).Complete && surviving.registry.DirWatched(detachedRoot, "child")
	})

	survivingIno := dirInode(t, surviving.root)
	detachedIno := dirInode(t, detachedRoot)
	detachedSubIno := dirInode(t, detachedSub)

	before := inotifyWatchedInodes(t)
	if !before[survivingIno] || !before[detachedIno] || !before[detachedSubIno] {
		t.Fatalf("missing expected inodes before close: surviving=%v detached=%v sub=%v",
			before[survivingIno], before[detachedIno], before[detachedSubIno])
	}

	surviving.registry.CloseRoot(detachedRoot)
	after := inotifyWatchedInodes(t)
	if after[detachedIno] || after[detachedSubIno] {
		t.Fatalf("detached inodes leaked in /proc/self/fdinfo: detached=%v sub=%v", after[detachedIno], after[detachedSubIno])
	}
	if !after[survivingIno] {
		t.Fatal("surviving root inode was dropped on detached root close")
	}
	testutil.FailErr(t, "write surviving file", os.WriteFile(filepath.Join(surviving.root, "kept.txt"), []byte("ok"), 0o644))
	surviving.waitFor("kept.txt", WorktreeChangeUnknown)
}

func TestInotifyRealBackendLossFaultsRecoversAndPreservesSurvivor(t *testing.T) {
	surviving := newInotifyRoot(t, 64)
	targetRoot, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve target root", err)

	var mu sync.Mutex
	var targetChanges []WorktreeChange
	unbind := RegisterObserver(func(_ context.Context, event Event) {
		if event.ProjectDir == targetRoot && event.Kind == WorktreeChanged && event.Source == SourceWatcher {
			mu.Lock()
			targetChanges = append(targetChanges, event.Changes...)
			mu.Unlock()
		}
	})
	t.Cleanup(unbind)

	surviving.registry.Ensure(t.Context(), targetRoot)
	testutil.WaitFor(t, "both roots complete coverage", func() bool {
		return surviving.registry.Coverage(surviving.root).Complete && surviving.registry.Coverage(targetRoot).Complete
	})

	// Drop targetRoot's watch directly from the real kernel inotify backend
	// while keeping the directory and its inode intact (same-inode loss).
	sharedFsnotify.ops.Lock()
	fw := sharedFsnotify.fw
	sharedFsnotify.ops.Unlock()
	if fw == nil {
		t.Fatal("shared inotify backend stream is nil")
	}
	testutil.FailErr(t, "remove target from backend stream", fw.Remove(streamPath(targetRoot)))

	// Target transitions to faulted; surviving root stays live and complete.
	if cov := surviving.registry.Coverage(targetRoot); !cov.Watching || !cov.Faulted || cov.Complete {
		t.Fatalf("target coverage after real backend loss = %+v, want Faulted=true Complete=false", cov)
	}
	if cov := surviving.registry.Coverage(surviving.root); !cov.Watching || cov.Faulted || !cov.Complete {
		t.Fatalf("surviving root disrupted by peer backend loss: %+v", cov)
	}

	// Rebind targetRoot via Ensure and verify positive settled delivery on both roots.
	surviving.registry.Ensure(t.Context(), targetRoot)
	testutil.WaitFor(t, "target recovers complete coverage", func() bool {
		cov := surviving.registry.Coverage(targetRoot)
		return cov.Watching && !cov.Faulted && cov.Complete
	})

	mu.Lock()
	targetChanges = nil
	mu.Unlock()
	testutil.FailErr(t, "write recovered target file", os.WriteFile(filepath.Join(targetRoot, "recovered.txt"), []byte("live"), 0o644))
	testutil.WaitFor(t, "recovered target delivers write", func() bool {
		ResetDebouncerForTest(t.Context())
		mu.Lock()
		defer mu.Unlock()
		return slices.ContainsFunc(targetChanges, func(ch WorktreeChange) bool {
			return ch.Path == "recovered.txt"
		})
	})

	testutil.FailErr(t, "write surviving file", os.WriteFile(filepath.Join(surviving.root, "still-live.txt"), []byte("ok"), 0o644))
	surviving.waitFor("still-live.txt", WorktreeChangeUnknown)
}
