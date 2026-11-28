package sourcecatalog

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
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

func TestIndexRejectsCachedForegroundSymlinkObservationAndPreservesForegroundReader(t *testing.T) {
	c, root := indexFixture(t)
	writeIndexFile(t, root.Path, "src/canon.go", "package src\n")
	writeIndexFile(t, root.Path, "real_dir/ok.go", "package ok\n")
	testutil.FailErr(t, "dir symlink", os.Symlink("src", filepath.Join(root.Path, "link_to_dir")))

	reader := waitIndex(t, c, root)
	scope := FileScope{Audience: HumanAudience, IncludeHidden: true}
	if got := indexedPaths(t, reader, scope); !slices.Equal(got, []string{"real_dir/ok.go", "src/canon.go"}) {
		t.Fatalf("initial files=%v", got)
	}

	// Simulate an unrelated foreground reader populating the shared directory observation
	// cache through the in-root symlink; foreground listing must keep working while
	// catalog reconciliation refuses to publish alias descendants.
	store, err := c.Trees.indexStore(t.Context(), "p", root)
	testutil.FailErr(t, "index store", err)
	obs, err := c.Trees.Directories.observeUntil(t.Context(), store, "link_to_dir", 0, 0)
	testutil.FailErr(t, "foreground symlink observation", err)
	if !obs.Complete || obs.Entries != 1 {
		t.Fatalf("foreground symlink observation unexpected: complete=%v entries=%d", obs.Complete, obs.Entries)
	}

	writeIndexFile(t, root.Path, "real_dir/added.go", "package ok\n")
	c.InvalidateRoot(root.Path, "link_to_dir", "real_dir")

	after := waitIndex(t, c, root)
	got := indexedPaths(t, after, scope)
	want := []string{"real_dir/added.go", "real_dir/ok.go", "src/canon.go"}
	if !slices.Equal(got, want) {
		t.Fatalf("post-reconcile files=%v want=%v", got, want)
	}
	var frontierCount, faultCount int
	testutil.FailErr(t, "frontier count", after.tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM frontier").Scan(&frontierCount))
	testutil.FailErr(t, "fault count", after.tx.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM faults").Scan(&faultCount))
	if frontierCount != 0 || faultCount != 0 || !after.Status.Complete || after.Status.Refreshing {
		t.Fatalf("frontier=%d faults=%d complete=%v refreshing=%v", frontierCount, faultCount, after.Status.Complete, after.Status.Refreshing)
	}
}

func TestIndexStampMismatchRetryRecomputesBoundedCoverageOnRetainedObservation(t *testing.T) {
	t.Run("unbounded_first_to_bounded_second", func(t *testing.T) {
		c, root := indexFixture(t)
		c.SetScopes(fixedScopes{budgets: sandbox.SurveyBudgets{
			DirectoryEntries: 2,
			SubtreeEntries:   1000,
			WalkEntries:      100000,
		}})
		writeIndexFile(t, root.Path, "slot/only.go", "package slot\n")

		store, err := c.Trees.indexStore(t.Context(), "p", root)
		testutil.FailErr(t, "index store", err)
		first, err := c.Trees.Directories.observeUntil(t.Context(), store, "slot", 0, 0)
		testutil.FailErr(t, "seed small cached observation", err)
		if !first.Complete || first.Entries != 1 {
			t.Fatalf("unexpected first observation: complete=%v entries=%d", first.Complete, first.Entries)
		}

		// Replace slot with a new directory inode that exceeds DirectoryEntries (2).
		testutil.FailErr(t, "remove small slot", os.RemoveAll(filepath.Join(root.Path, "slot")))
		writeIndexFile(t, root.Path, "slot/f1.go", "package slot\n")
		writeIndexFile(t, root.Path, "slot/f2.go", "package slot\n")
		writeIndexFile(t, root.Path, "slot/f3.go", "package slot\n")
		writeIndexFile(t, root.Path, "slot/f4.go", "package slot\n")

		reader := waitIndex(t, c, root)
		var refused string
		testutil.FailErr(t, "query slot refused", reader.tx.QueryRowContext(t.Context(),
			"SELECT refused FROM nodes WHERE path='slot'").Scan(&refused))
		if refused != string(sandbox.BoundaryDirectoryCap) {
			t.Fatalf("slot refused=%q want=%q (bounded must be recomputed on retained retry observation)", refused, sandbox.BoundaryDirectoryCap)
		}
		scope := FileScope{Audience: HumanAudience, IncludeHidden: true}
		if got := indexedPaths(t, reader, scope); len(got) != 0 {
			t.Fatalf("boundary directory must hold no cataloged children, got=%v", got)
		}
	})

	t.Run("bounded_first_to_unbounded_second", func(t *testing.T) {
		c, root := indexFixture(t)
		c.SetScopes(fixedScopes{budgets: sandbox.SurveyBudgets{
			DirectoryEntries: 2,
			SubtreeEntries:   1000,
			WalkEntries:      100000,
		}})
		writeIndexFile(t, root.Path, "slot/f1.go", "package slot\n")
		writeIndexFile(t, root.Path, "slot/f2.go", "package slot\n")
		writeIndexFile(t, root.Path, "slot/f3.go", "package slot\n")
		writeIndexFile(t, root.Path, "slot/f4.go", "package slot\n")

		store, err := c.Trees.indexStore(t.Context(), "p", root)
		testutil.FailErr(t, "index store", err)
		first, err := c.Trees.Directories.observeUntil(t.Context(), store, "slot", 3, 0)
		testutil.FailErr(t, "seed bounded cached observation", err)
		if first.Complete || first.Entries < 3 {
			t.Fatalf("unexpected first observation: complete=%v entries=%d", first.Complete, first.Entries)
		}

		// Replace slot with a new small directory inode below DirectoryEntries (2).
		testutil.FailErr(t, "remove large slot", os.RemoveAll(filepath.Join(root.Path, "slot")))
		writeIndexFile(t, root.Path, "slot/fresh.go", "package slot\n")

		reader := waitIndex(t, c, root)
		scope := FileScope{Audience: HumanAudience, IncludeHidden: true}
		got := indexedPaths(t, reader, scope)
		want := []string{"slot/fresh.go"}
		if !slices.Equal(got, want) {
			t.Fatalf("indexed files=%v want=%v", got, want)
		}
		var refused string
		testutil.FailErr(t, "query slot refused", reader.tx.QueryRowContext(t.Context(),
			"SELECT refused FROM nodes WHERE path='slot'").Scan(&refused))
		if refused != "" || !reader.Status.Complete || reader.Status.Refreshing {
			t.Fatalf("slot refused=%q complete=%v refreshing=%v, want empty/true/false", refused, reader.Status.Complete, reader.Status.Refreshing)
		}
	})
}
