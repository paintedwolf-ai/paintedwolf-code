package sourcecatalog

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestIndexIncrementalReconcileDoesNotTraverseInRootDirSymlink(t *testing.T) {
	c, root := indexFixture(t)
	writeIndexFile(t, root.Path, "src/a.go", "package src\n")
	writeIndexFile(t, root.Path, "src/sub/b.go", "package sub\n")
	writeIndexFile(t, root.Path, "real_dir/c.go", "package realdir\n")
	testutil.FailErr(t, "file symlink", os.Symlink("src/a.go", filepath.Join(root.Path, "link_to_file")))
	testutil.FailErr(t, "dir symlink", os.Symlink("src", filepath.Join(root.Path, "link_to_dir")))

	reader := waitIndex(t, c, root)
	scope := FileScope{Audience: HumanAudience, IncludeHidden: true}
	initial := indexedPaths(t, reader, scope)
	wantInitial := []string{"real_dir/c.go", "src/a.go", "src/sub/b.go"}
	if !slices.Equal(initial, wantInitial) {
		t.Fatalf("initial files=%v want=%v", initial, wantInitial)
	}

	// Simulate watcher-delivered incremental paths naming the directory symlink,
	// a descendant through the symlink, a file symlink, and a real directory control.
	writeIndexFile(t, root.Path, "real_dir/d.go", "package realdir\n")
	c.InvalidateRoot(root.Path, "link_to_dir", "link_to_dir/sub", "link_to_file", "real_dir")

	after := waitIndex(t, c, root)
	got := indexedPaths(t, after, scope)
	want := []string{"real_dir/c.go", "real_dir/d.go", "src/a.go", "src/sub/b.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("post-reconcile files=%v want=%v", got, want)
	}
	count, err := after.FileCount(t.Context(), scope)
	testutil.FailErr(t, "file count", err)
	if count != 4 {
		t.Fatalf("file count=%d want=4", count)
	}

	// Verify symlink leaf rows and empty frontier/faults directly in the pinned index generation.
	var isDir, isSym bool
	testutil.FailErr(t, "query link_to_dir", after.tx.QueryRowContext(t.Context(),
		"SELECT is_dir, symlink FROM nodes WHERE path='link_to_dir'").Scan(&isDir, &isSym))
	if !isSym || !isDir {
		t.Fatalf("link_to_dir row: is_dir=%v symlink=%v, want true/true", isDir, isSym)
	}
	testutil.FailErr(t, "query link_to_file", after.tx.QueryRowContext(t.Context(),
		"SELECT is_dir, symlink FROM nodes WHERE path='link_to_file'").Scan(&isDir, &isSym))
	if !isSym || isDir {
		t.Fatalf("link_to_file row: is_dir=%v symlink=%v, want false/true", isDir, isSym)
	}
	var frontierCount, faultCount int
	testutil.FailErr(t, "frontier count", after.tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM frontier").Scan(&frontierCount))
	testutil.FailErr(t, "fault count", after.tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM faults").Scan(&faultCount))
	if frontierCount != 0 || faultCount != 0 || !after.Status.Complete || after.Status.Refreshing {
		t.Fatalf("frontier=%d faults=%d complete=%v refreshing=%v", frontierCount, faultCount, after.Status.Complete, after.Status.Refreshing)
	}
}

func TestIndexDirectoryReplacedBySymlinkConvergesStaleAliasRows(t *testing.T) {
	c, root := indexFixture(t)
	writeIndexFile(t, root.Path, "src/canon.go", "package src\n")
	writeIndexFile(t, root.Path, "alias_slot/stale.go", "package stale\n")

	reader := waitIndex(t, c, root)
	scope := FileScope{Audience: HumanAudience, IncludeHidden: true}
	if got := indexedPaths(t, reader, scope); !slices.Equal(got, []string{"alias_slot/stale.go", "src/canon.go"}) {
		t.Fatalf("initial files=%v", got)
	}

	// Replace real directory with an in-root symlink to src and trigger incremental reconcile.
	testutil.FailErr(t, "remove real dir", os.RemoveAll(filepath.Join(root.Path, "alias_slot")))
	testutil.FailErr(t, "replace with dir symlink", os.Symlink("src", filepath.Join(root.Path, "alias_slot")))
	c.InvalidateRoot(root.Path, "alias_slot")

	after := waitIndex(t, c, root)
	got := indexedPaths(t, after, scope)
	want := []string{"src/canon.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("post-replacement files=%v want=%v", got, want)
	}
	count, err := after.FileCount(t.Context(), scope)
	testutil.FailErr(t, "file count", err)
	if count != 1 {
		t.Fatalf("file count=%d want=1", count)
	}

	var isDir, isSym bool
	testutil.FailErr(t, "query alias_slot", after.tx.QueryRowContext(t.Context(),
		"SELECT is_dir, symlink FROM nodes WHERE path='alias_slot'").Scan(&isDir, &isSym))
	if !isSym || !isDir {
		t.Fatalf("alias_slot row: is_dir=%v symlink=%v, want true/true", isDir, isSym)
	}
	var frontierCount, faultCount int
	testutil.FailErr(t, "frontier count", after.tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM frontier").Scan(&frontierCount))
	testutil.FailErr(t, "fault count", after.tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM faults").Scan(&faultCount))
	if frontierCount != 0 || faultCount != 0 || !after.Status.Complete || after.Status.Refreshing {
		t.Fatalf("frontier=%d faults=%d complete=%v refreshing=%v", frontierCount, faultCount, after.Status.Complete, after.Status.Refreshing)
	}
}
