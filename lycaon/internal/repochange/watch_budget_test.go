package repochange

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watchfd"
)

// newBudgetedRegistry returns a registry capped at `descriptors`.
func newBudgetedRegistry(descriptors int) *WatcherRegistry {
	return &WatcherRegistry{
		byDir:   map[string]*worktreeWatcher{},
		maxDirs: watchfd.MaxDirs,
		budget:  watchfd.NewBudget(descriptors),
	}
}

// skipUnlessBudgeted skips a test whose claim is that a short budget leaves
// part of the tree unwatched; a recursive stream has no such shortfall.
func skipUnlessBudgeted(t *testing.T) {
	t.Helper()
	if watchfd.Recursive {
		t.Skip("a recursive root stream covers the tree without a per-directory budget")
	}
}

// fanoutWorktree builds `dirs` directories with `filesPerDir` files each.
func fanoutWorktree(t *testing.T, dirs, filesPerDir int) string {
	t.Helper()
	root := t.TempDir()
	testutil.FailErr(t, "mkdir .git", os.Mkdir(filepath.Join(root, ".git"), 0o755))
	for d := range dirs {
		dir := filepath.Join(root, fmt.Sprintf("d%03d", d))
		testutil.FailErr(t, "mkdir tree dir", os.Mkdir(dir, 0o755))
		for f := range filesPerDir {
			name := filepath.Join(dir, fmt.Sprintf("f%03d", f))
			testutil.FailErr(t, "write tree file", os.WriteFile(name, []byte("x"), 0o644))
		}
	}
	return root
}

func (r *WatcherRegistry) spentForTest() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	total := 0
	for _, w := range r.byDir {
		total += w.spentForTest()
	}
	return total
}

func (w *worktreeWatcher) spentForTest() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.spent
}

func (w *worktreeWatcher) watchesForTest(path string) bool {
	for _, got := range w.watcher.WatchList() {
		if got == path {
			return true
		}
	}
	return false
}

func fanoutWatchDirectories(root string, dirs, filesPerDir int) []WatchDirectory {
	out := make([]WatchDirectory, 0, dirs+1)
	out = append(out, WatchDirectory{Path: root, EntryCount: dirs + 1}) // .git plus source directories.
	for d := range dirs {
		out = append(out, WatchDirectory{
			Path:       filepath.Join(root, fmt.Sprintf("d%03d", d)),
			EntryCount: filesPerDir,
		})
	}
	return out
}

func TestWideRootDoesNotBypassDescriptorCeiling(t *testing.T) {
	if watchfd.Cost(2) == watchfd.Cost(1) {
		t.Skip("directory width has no descriptor cost on this platform")
	}
	root := t.TempDir()
	for i := range watchfd.MaxDirEntries + 1 {
		name := filepath.Join(root, fmt.Sprintf("f%03d", i))
		testutil.FailErr(t, "write wide root", os.WriteFile(name, []byte("x"), 0o644))
	}

	reg := newBudgetedRegistry(4096)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)

	reg.mu.Lock()
	w := reg.byDir[root]
	reg.mu.Unlock()
	if w == nil {
		t.Fatal("wide root has no watcher state")
	}
	if w.watchesForTest(root) {
		t.Fatalf("root with %d entries bypassed the %d-entry descriptor ceiling",
			watchfd.MaxDirEntries+1, watchfd.MaxDirEntries)
	}
	if spent := reg.spentForTest(); spent != 0 {
		t.Fatalf("wide root spent %d descriptors, want 0", spent)
	}
	if coverage := reg.Coverage(root); coverage.Complete || coverage.Truncated == 0 {
		t.Fatalf("coverage = %+v, want wide root delegated to polling", coverage)
	}
}

func TestGrowingDirectoryReturnsWatcherBudgetAtCeiling(t *testing.T) {
	if watchfd.Cost(2) == watchfd.Cost(1) {
		t.Skip("directory width has no descriptor cost on this platform")
	}
	root := t.TempDir()
	reg := newBudgetedRegistry(4096)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)

	reg.mu.Lock()
	w := reg.byDir[root]
	reg.mu.Unlock()
	if w == nil || !w.watchesForTest(root) {
		t.Fatal("narrow root was not watched")
	}
	before := reg.budget.Remaining()
	if w.repriceDir(root, watchfd.Cost(watchfd.MaxDirEntries+1)) {
		t.Fatal("directory remained watched after growing beyond the entry ceiling")
	}
	if w.watchesForTest(root) {
		t.Fatal("over-wide directory remained in the filesystem watcher")
	}
	if after := reg.budget.Remaining(); after <= before {
		t.Fatalf("budget did not recover after dropping wide directory: %d -> %d", before, after)
	}
}

func TestWatcherStaysWithinDescriptorBudget(t *testing.T) {
	const budget = 64
	const dirs = 40
	const filesPerDir = 50
	root := fanoutWorktree(t, dirs, filesPerDir)

	reg := newBudgetedRegistry(budget)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)
	reg.Seed(t.Context(), root, fanoutWatchDirectories(root, dirs, filesPerDir))

	if spent := reg.spentForTest(); spent > budget {
		t.Fatalf("watcher spent %d descriptors, budget is %d", spent, budget)
	}
	if reg.budget.Remaining() < 0 {
		t.Fatal("budget went negative")
	}
}

func TestWatcherRegistersRootFirst(t *testing.T) {
	root := fanoutWorktree(t, 40, 50)

	reg := newBudgetedRegistry(64)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)

	reg.mu.Lock()
	w, ok := reg.byDir[root]
	reg.mu.Unlock()
	if !ok {
		t.Fatal("no watcher registered for the worktree root")
	}
	w.mu.Lock()
	dirs := len(w.watched)
	w.mu.Unlock()
	if dirs == 0 {
		t.Fatal("root was not registered under a tight budget")
	}
}

func TestWatcherReturnsBudgetOnClose(t *testing.T) {
	skipUnlessBudgeted(t)
	const budget = 512
	root := fanoutWorktree(t, 8, 4)

	reg := newBudgetedRegistry(budget)
	reg.Ensure(t.Context(), root)
	if reg.budget.Remaining() == budget {
		t.Fatal("watcher took nothing from the budget")
	}

	reg.CloseAll()
	if got := reg.budget.Remaining(); got != budget {
		t.Fatalf("budget after close = %d, want %d", got, budget)
	}
}

func TestWatcherDescendsThroughWideDirectories(t *testing.T) {
	skipUnlessBudgeted(t)
	root := t.TempDir()
	testutil.FailErr(t, "mkdir .git", os.Mkdir(filepath.Join(root, ".git"), 0o755))

	wide := filepath.Join(root, "wide")
	testutil.FailErr(t, "mkdir wide", os.Mkdir(wide, 0o755))
	for f := range watchfd.MaxDirEntries * 2 {
		name := filepath.Join(wide, fmt.Sprintf("f%04d", f))
		testutil.FailErr(t, "write wide file", os.WriteFile(name, []byte("x"), 0o644))
	}
	narrow := filepath.Join(wide, "narrow")
	testutil.FailErr(t, "mkdir narrow", os.Mkdir(narrow, 0o755))

	reg := newBudgetedRegistry(4096)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)
	reg.Seed(t.Context(), root, []WatchDirectory{
		{Path: root, EntryCount: 2},
		{Path: wide, EntryCount: watchfd.MaxDirEntries*2 + 1},
		{Path: narrow, EntryCount: 0},
	})

	reg.mu.Lock()
	w := reg.byDir[root]
	reg.mu.Unlock()
	if w == nil {
		t.Fatal("no watcher registered for the worktree root")
	}

	// Per-entry backends skip wide directories; unit-cost backends watch them.
	if watchfd.Cost(2) != watchfd.Cost(1) && w.watchesForTest(wide) {
		t.Fatalf("wide directory is watched: %d entries costs %d descriptors, over the %d ceiling",
			watchfd.MaxDirEntries*2, watchfd.Cost(watchfd.MaxDirEntries*2), watchfd.Cost(watchfd.MaxDirEntries))
	}
	if !w.watchesForTest(narrow) {
		t.Fatal("directory under a wide directory is unwatched")
	}
}

// A short budget lands on shallow directories. The catalog arrives
// deepest-first so input order cannot be what passes.
func TestWatcherSpendsShortBudgetOnShallowDirectories(t *testing.T) {
	skipUnlessBudgeted(t)
	root := t.TempDir()
	testutil.FailErr(t, "mkdir .git", os.Mkdir(filepath.Join(root, ".git"), 0o755))

	mkdir := func(parts ...string) string {
		path := filepath.Join(append([]string{root}, parts...)...)
		testutil.FailErr(t, "mkdir "+strings.Join(parts, "/"), os.MkdirAll(path, 0o755))
		return path
	}
	app := mkdir("app")
	lib := mkdir("lib")
	dep := mkdir("dep")
	deep := mkdir("dep", "n1", "n2", "n3")

	catalog := []WatchDirectory{
		{Path: deep}, {Path: filepath.Join(root, "dep", "n1", "n2")},
		{Path: filepath.Join(root, "dep", "n1")}, {Path: dep},
		{Path: lib}, {Path: app}, {Path: root},
	}

	// The root contains .git, app, lib, and dep. Past that measured root and
	// the empty .git ref surface, exactly three empty registrations fit.
	reg := newBudgetedRegistry(watchfd.Cost(4) + watchfd.Cost(0) + 3)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)
	reg.Seed(t.Context(), root, catalog)

	reg.mu.Lock()
	w := reg.byDir[root]
	reg.mu.Unlock()
	if w == nil {
		t.Fatal("no watcher registered for the worktree root")
	}
	for _, dir := range []string{app, lib} {
		if !w.watchesForTest(dir) {
			t.Fatalf("shallow source directory %s lost the budget to a deeper tree", dir)
		}
	}
	if w.watchesForTest(deep) {
		t.Fatalf("deep directory %s took budget a shallow source directory needed", deep)
	}

	coverage := reg.Coverage(root)
	if coverage.Complete || coverage.Truncated == 0 {
		t.Fatalf("coverage = %+v, want the unregistered tail reported as truncated", coverage)
	}
}

func TestWatcherWithNoBudgetWatchesNothing(t *testing.T) {
	root := fanoutWorktree(t, 4, 4)

	reg := newBudgetedRegistry(0)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)

	if spent := reg.spentForTest(); spent != 0 {
		t.Fatalf("watcher spent %d descriptors with a zero budget", spent)
	}
}

func TestCoverageReportsWatchTruncation(t *testing.T) {
	skipUnlessBudgeted(t)
	const budget = 32
	const dirs = 60
	const filesPerDir = 20
	root := fanoutWorktree(t, dirs, filesPerDir)

	reg := newBudgetedRegistry(budget)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)
	reg.Seed(t.Context(), root, fanoutWatchDirectories(root, dirs, filesPerDir))

	coverage := reg.Coverage(root)
	if !coverage.Watching {
		t.Fatal("no watcher for the seeded root")
	}
	if coverage.Complete || coverage.Truncated == 0 {
		t.Fatalf("coverage = %+v, want a truncated watch under a tight budget", coverage)
	}
}

func TestCoverageIsCompleteWhenEverythingRegisters(t *testing.T) {
	root := fanoutWorktree(t, 3, 2)

	reg := newBudgetedRegistry(4096)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)
	reg.Seed(t.Context(), root, fanoutWatchDirectories(root, 3, 2))

	coverage := reg.Coverage(root)
	if !coverage.Watching || !coverage.Complete || coverage.Truncated != 0 {
		t.Fatalf("coverage = %+v, want a complete watch", coverage)
	}
	if coverage.Watched == 0 {
		t.Fatal("complete coverage reported no watched directories")
	}
}

func TestCoverageIsNotCompleteForAnUnwatchedRoot(t *testing.T) {
	reg := newBudgetedRegistry(4096)
	t.Cleanup(reg.CloseAll)
	coverage := reg.Coverage(t.TempDir())
	if coverage.Watching || coverage.Complete {
		t.Fatalf("coverage = %+v, want an unwatched root to report incomplete", coverage)
	}
}

func TestReseedingChargesTheBudgetOnce(t *testing.T) {
	const dirs = 8
	const filesPerDir = 4
	root := fanoutWorktree(t, dirs, filesPerDir)
	directories := fanoutWatchDirectories(root, dirs, filesPerDir)

	reg := newBudgetedRegistry(4096)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)
	reg.Seed(t.Context(), root, directories)
	spentOnce := reg.spentForTest()
	watchedOnce := reg.Coverage(root).Watched

	// An SSE reconnect re-runs the same seed with the same catalog.
	for range 5 {
		reg.Seed(t.Context(), root, directories)
	}

	if spent := reg.spentForTest(); spent != spentOnce {
		t.Fatalf("re-seeding moved spent budget from %d to %d", spentOnce, spent)
	}
	coverage := reg.Coverage(root)
	if coverage.Watched != watchedOnce {
		t.Fatalf("re-seeding moved watched dirs from %d to %d", watchedOnce, coverage.Watched)
	}
	if !coverage.Complete {
		t.Fatalf("coverage = %+v, want re-seeding to keep coverage complete", coverage)
	}
}

func TestTruncatedCoverageHealsWhenCapacityFrees(t *testing.T) {
	skipUnlessBudgeted(t)
	const dirs = 60
	const filesPerDir = 20
	crowded := fanoutWorktree(t, dirs, filesPerDir)
	small := fanoutWorktree(t, 2, 2)

	// The crowded root drains the budget, leaving the small root truncated:
	// exactly the crowded root plus the small root's own directory fits.
	budget := watchfd.Cost(dirs+1) + dirs*watchfd.Cost(filesPerDir) + watchfd.Cost(3)
	reg := newBudgetedRegistry(budget)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), crowded)
	reg.Seed(t.Context(), crowded, fanoutWatchDirectories(crowded, dirs, filesPerDir))
	reg.Ensure(t.Context(), small)
	reg.Seed(t.Context(), small, fanoutWatchDirectories(small, 2, 2))
	if coverage := reg.Coverage(small); coverage.Complete {
		t.Fatalf("coverage = %+v, want the drained budget to truncate the small root", coverage)
	}

	reg.CloseRoot(crowded)
	reg.Seed(t.Context(), small, fanoutWatchDirectories(small, 2, 2))

	if coverage := reg.Coverage(small); !coverage.Complete {
		t.Fatalf("coverage = %+v, want a re-seed after freed capacity to heal", coverage)
	}
	reg.mu.Lock()
	w := reg.byDir[small]
	reg.mu.Unlock()
	if w == nil || !w.watchesForTest(small) {
		t.Fatal("healed coverage left the root directory itself unwatched")
	}
}

func TestCoverageShortfallDoesNotPublishMutation(t *testing.T) {
	root := t.TempDir()
	var changes atomic.Int32
	RegisterObserver(func(_ context.Context, event Event) {
		if event.ProjectDir == root && event.Kind == WorktreeChanged {
			changes.Add(1)
		}
	})
	defer ResetObserversForTest()
	defer ResetDebouncerForTest(context.Background())
	watcher := &worktreeWatcher{root: root, watched: make(map[string]int)}
	before := CurrentEpoch(root)
	for _, count := range []int{4, 9, 0} {
		watcher.setTruncated(count)
		if got := watcher.coverage().Truncated; got != count {
			t.Fatalf("coverage shortfall = %d, want %d", got, count)
		}
	}
	ResetDebouncerForTest(t.Context())
	if changes.Load() != 0 || CurrentEpoch(root) != before {
		t.Fatal("coverage change published a filesystem mutation")
	}
}
