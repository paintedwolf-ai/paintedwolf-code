package repochange

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/watchfd"
)

// Shallow-first watching preserves nearby directories under a cap.
func TestDirWatchedSeparatesWatchedFromUnwatchedUnderTruncation(t *testing.T) {
	skipUnlessBudgeted(t)
	const dirs = 60
	const filesPerDir = 20
	root := fanoutWorktree(t, dirs, filesPerDir)

	// Exactly the root plus three of its children fit.
	budget := watchfd.Cost(dirs+1) + 3*watchfd.Cost(filesPerDir)
	reg := newBudgetedRegistry(budget)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)
	reg.Seed(t.Context(), root, fanoutWatchDirectories(root, dirs, filesPerDir))

	if coverage := reg.Coverage(root); coverage.Complete {
		t.Fatalf("coverage = %+v, want a truncated watch under a tight budget", coverage)
	}
	if !reg.DirWatched(root, ".") {
		t.Fatal("the root directory is watched but DirWatched reported otherwise")
	}
	if !reg.DirWatched(root, "d000") {
		t.Fatal("the first seeded directory is watched but DirWatched reported otherwise")
	}
	last := fmt.Sprintf("d%03d", dirs-1)
	if reg.DirWatched(root, last) {
		t.Fatalf("%s is past the budget but DirWatched claimed a watch", last)
	}
}

func TestDirWatchedReportsEverySeededDirectory(t *testing.T) {
	const dirs = 4
	const filesPerDir = 2
	root := fanoutWorktree(t, dirs, filesPerDir)

	reg := newBudgetedRegistry(4096)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)
	reg.Seed(t.Context(), root, fanoutWatchDirectories(root, dirs, filesPerDir))

	if coverage := reg.Coverage(root); !coverage.Complete {
		t.Fatalf("coverage = %+v, want a complete watch", coverage)
	}
	for d := range dirs {
		dir := fmt.Sprintf("d%03d", d)
		if !reg.DirWatched(root, dir) {
			t.Fatalf("%s is seeded under a complete watch but DirWatched reported no watch", dir)
		}
	}
	// A recursive stream covers a directory by position, so only a
	// per-directory backend distinguishes seeded from unseeded.
	if !watchfd.Recursive && reg.DirWatched(root, "never-seeded") {
		t.Fatal("DirWatched claimed a watch on a directory that was never seeded")
	}
}

// Directories beyond the watch budget remain unwatched.
func TestDirWatchedIsFalseForASkippedWideDirectory(t *testing.T) {
	if watchfd.Cost(2) == watchfd.Cost(1) {
		t.Skip("this backend charges one descriptor per directory; nothing is skipped for width")
	}
	root := t.TempDir()
	testutil.FailErr(t, "mkdir .git", os.Mkdir(filepath.Join(root, ".git"), 0o755))
	wide := filepath.Join(root, "wide")
	testutil.FailErr(t, "mkdir wide", os.Mkdir(wide, 0o755))
	narrow := filepath.Join(wide, "narrow")
	testutil.FailErr(t, "mkdir narrow", os.Mkdir(narrow, 0o755))

	reg := newBudgetedRegistry(4096)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)
	reg.Seed(t.Context(), root, []WatchDirectory{
		{Path: root, EntryCount: 1},
		{Path: wide, EntryCount: watchfd.MaxDirEntries*2 + 1},
		{Path: narrow, EntryCount: 0},
	})

	if reg.DirWatched(root, "wide") {
		t.Fatal("a directory skipped for width reported a watch")
	}
	if !reg.DirWatched(root, "wide/narrow") {
		t.Fatal("a narrow directory under a skipped wide one reported no watch")
	}
}

func TestDirWatchedIsFalseForAnUnwatchedRoot(t *testing.T) {
	reg := newBudgetedRegistry(4096)
	t.Cleanup(reg.CloseAll)
	if reg.DirWatched(t.TempDir(), ".") {
		t.Fatal("a root with no watcher reported a watched directory")
	}
}

func TestDirWatchedRejectsDirectoriesOutsideTheRoot(t *testing.T) {
	root := fanoutWorktree(t, 2, 1)
	reg := newBudgetedRegistry(4096)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)
	reg.Seed(t.Context(), root, fanoutWatchDirectories(root, 2, 1))

	for _, dir := range []string{"..", "../..", "d000/../.."} {
		if reg.DirWatched(root, dir) {
			t.Fatalf("DirWatched(%q) escaped the root and reported a watch", dir)
		}
	}
}

// The wire carries the root as "." and children slash-separated; both must land
// on the same registration the watcher holds.
func TestDirWatchedAcceptsTheWireSpellingOfADirectory(t *testing.T) {
	root := t.TempDir()
	testutil.FailErr(t, "mkdir .git", os.Mkdir(filepath.Join(root, ".git"), 0o755))
	nested := filepath.Join(root, "a", "b")
	testutil.FailErr(t, "mkdir nested", os.MkdirAll(nested, 0o755))

	reg := newBudgetedRegistry(4096)
	t.Cleanup(reg.CloseAll)
	reg.Ensure(t.Context(), root)
	reg.Seed(t.Context(), root, []WatchDirectory{
		{Path: root, EntryCount: 1},
		{Path: filepath.Join(root, "a"), EntryCount: 1},
		{Path: nested, EntryCount: 0},
	})

	for _, dir := range []string{"", ".", "./"} {
		if !reg.DirWatched(root, dir) {
			t.Fatalf("DirWatched(%q) did not resolve to the root directory", dir)
		}
	}
	for _, dir := range []string{"a/b", "./a/b", "a/b/"} {
		if !reg.DirWatched(root, dir) {
			t.Fatalf("DirWatched(%q) did not resolve to the nested directory", dir)
		}
	}
}
