package sourcecatalog

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/repochange"
	"github.com/lycaon/lycaon/internal/testutil"
)

// countedCatalog counts full walks; incremental reconciliation never calls build.
func countedCatalog(t *testing.T, during func(call int32, root Root)) (*Catalog, Root, *atomic.Int32) {
	t.Helper()
	root := Root{ID: "root", Path: t.TempDir()}
	t.Cleanup(repochange.ResetWatchersForTest)
	repochange.MarkCoverageCompleteForTest(root.Path)
	writeCatalogFile(t, root.Path, "a.txt")
	catalog := New()
	var walks atomic.Int32
	catalog.build = func(ctx context.Context, roots []Root, policy walkPolicy) (Snapshot, error) {
		call := walks.Add(1)
		snapshot, err := buildSnapshot(ctx, roots, policy)
		if during != nil {
			during(call, roots[0])
		}
		return snapshot, err
	}
	return catalog, root, &walks
}

func writeCatalogFile(t *testing.T, root, rel string) {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(abs), 0o755))
	testutil.FailErr(t, "write "+rel, os.WriteFile(abs, []byte(rel), 0o644))
}

func settledSnapshot(t *testing.T, catalog *Catalog, root Root) Snapshot {
	t.Helper()
	snapshot, err := catalog.Observe(t.Context(), "p", []Root{root})
	testutil.FailErr(t, "observe", err)
	if snapshot.Moving {
		snapshot, err = catalog.Snapshot(t.Context(), "p", []Root{root})
		testutil.FailErr(t, "join reconciliation", err)
	}
	return snapshot
}

// An index write or ref move advances the root's epoch without touching the
// tree; the catalog classifies it and keeps its generation.
func TestClassifiedEpochAdvanceKeepsGenerationWithoutWalk(t *testing.T) {
	catalog, root, walks := countedCatalog(t, nil)
	first, err := catalog.Snapshot(t.Context(), "p", []Root{root})
	testutil.FailErr(t, "first generation", err)

	repochange.Advance(root.Path)
	catalog.observeEpoch(root.Path)
	current, settled := catalog.CurrentSettled(t.Context(), "p", []Root{root})
	if !settled || current.Revision != first.Revision {
		t.Fatalf("after a classified advance settled=%v revision=%d, want settled at %d", settled, current.Revision, first.Revision)
	}
	if got := walks.Load(); got != 1 {
		t.Fatalf("walks = %d, want 1", got)
	}
}

// An advance not yet classified re-pins the generation once the watcher has
// nothing pending for the tree, instead of re-walking it.
func TestUnclassifiedEpochAdvanceRepinsWithoutWalk(t *testing.T) {
	catalog, root, walks := countedCatalog(t, nil)
	_, err := catalog.Snapshot(t.Context(), "p", []Root{root})
	testutil.FailErr(t, "first generation", err)

	repochange.Advance(root.Path)
	if _, settled := catalog.CurrentSettled(t.Context(), "p", []Root{root}); settled {
		t.Fatal("generation reported settled across an unclassified advance")
	}
	pinned := settledSnapshot(t, catalog, root)
	if pinned.Moving || !repochange.EpochCurrent(root.Path, pinned.Epochs[root.ID]) {
		t.Fatalf("generation not re-pinned: moving=%v epochs=%v", pinned.Moving, pinned.Epochs)
	}
	if got := walks.Load(); got != 1 {
		t.Fatalf("walks = %d, want 1", got)
	}
}

// A walk overtaken only by classified advances publishes pinned, not moving.
func TestWalkOvertakenByClassifiedAdvanceIsPinned(t *testing.T) {
	var catalog *Catalog
	catalog, root, walks := countedCatalog(t, func(_ int32, root Root) {
		repochange.Advance(root.Path)
		catalog.observeEpoch(root.Path)
	})
	snapshot, err := catalog.Snapshot(t.Context(), "p", []Root{root})
	testutil.FailErr(t, "generation", err)
	if snapshot.Moving || !repochange.EpochCurrent(root.Path, snapshot.Epochs[root.ID]) {
		t.Fatalf("generation moving=%v epochs=%v, want pinned at the current epoch", snapshot.Moving, snapshot.Epochs)
	}
	if got := walks.Load(); got != 1 {
		t.Fatalf("walks = %d, want 1", got)
	}
}

// A walk overtaken by a tracked write publishes moving and finishes by
// reconciling that path, not by walking the tree again.
func TestWalkOvertakenByTrackedWriteReconcilesIncrementally(t *testing.T) {
	var catalog *Catalog
	catalog, root, walks := countedCatalog(t, func(call int32, root Root) {
		if call != 1 {
			return
		}
		writeCatalogFile(t, root.Path, "late.txt")
		repochange.Advance(root.Path)
		catalog.InvalidateRootChange(root.Path, []string{"late.txt"}, repochange.StructuralPathSet{Paths: []string{"late.txt"}})
	})
	moving, err := catalog.Snapshot(t.Context(), "p", []Root{root})
	testutil.FailErr(t, "overtaken generation", err)
	if !moving.Moving {
		t.Fatal("overtaken walk published as settled")
	}
	settled := settledSnapshot(t, catalog, root)
	if _, ok := settled.Entry(root.ID, "late.txt"); !ok || settled.Moving {
		t.Fatalf("reconciled generation moving=%v, late.txt present=%v", settled.Moving, ok)
	}
	if got := walks.Load(); got != 1 {
		t.Fatalf("walks = %d, want 1", got)
	}
}

// A path under a boundary of the previous generation still counts while a new
// walk is in flight, because that walk may enter the directory.
func TestWriteDuringRewalkKeepsPathsBelowPreviousBoundaries(t *testing.T) {
	var catalog *Catalog
	catalog, root, _ := countedCatalog(t, func(call int32, root Root) {
		if call != 2 {
			return
		}
		writeCatalogFile(t, root.Path, "big/new.txt")
		repochange.Advance(root.Path)
		catalog.InvalidateRootChange(root.Path, []string{"big/new.txt"}, repochange.StructuralPathSet{Paths: []string{"big/new.txt"}})
	})
	writeCatalogFile(t, root.Path, "big/old.txt")
	first, err := catalog.Snapshot(t.Context(), "p", []Root{root})
	testutil.FailErr(t, "first generation", err)
	bounded := map[string]Entry{}
	for _, entry := range first.Entries {
		if entry.Path == "big" {
			entry.Boundary = true
		}
		if entry.Path != "big/old.txt" {
			bounded[entry.Path] = entry
		}
	}
	catalog.mu.Lock()
	rec := catalog.records[rootKey("p", root)]
	rec.snapshot = snapshotFromEntries(root, bounded).pinnedAt(root.ID, repochange.CurrentEpoch(root.Path))
	catalog.mu.Unlock()

	catalog.InvalidateRoot(root.Path)
	settled := settledSnapshot(t, catalog, root)
	if _, ok := settled.Entry(root.ID, "big/new.txt"); !ok {
		t.Fatal("write below a previous boundary was dropped during the rewalk")
	}
}

// A subtree generation inside a watched root trusts that root's watcher: it
// stays settled past the unwatched reuse window, ignores changes elsewhere in
// the root, and reconciles a change below it without walking again.
func TestSubtreeGenerationFollowsItsAttachedRootWatcher(t *testing.T) {
	catalog, root, walks := countedCatalog(t, nil)
	now := time.Now()
	catalog.now = func() time.Time { return now }
	writeCatalogFile(t, root.Path, "src/a.txt")
	scoped := Root{ID: "root\x00src", Path: filepath.Join(root.Path, "src"), Within: root.Path}
	_, err := catalog.Snapshot(t.Context(), "p", []Root{scoped})
	testutil.FailErr(t, "subtree generation", err)

	now = now.Add(incompleteCoverageTTL + time.Second)
	writeCatalogFile(t, root.Path, "other/b.txt")
	repochange.Advance(root.Path)
	catalog.InvalidateRootChange(root.Path, []string{"other/b.txt"}, repochange.StructuralPathSet{Paths: []string{"other/b.txt"}})
	if _, settled := catalog.CurrentSettled(t.Context(), "p", []Root{scoped}); !settled {
		t.Fatal("subtree generation unsettled by time or by a change outside it")
	}

	writeCatalogFile(t, root.Path, "src/c.txt")
	repochange.Advance(root.Path)
	catalog.InvalidateRootChange(root.Path, []string{"src/c.txt"}, repochange.StructuralPathSet{Paths: []string{"src/c.txt"}})
	if _, settled := catalog.CurrentSettled(t.Context(), "p", []Root{scoped}); settled {
		t.Fatal("subtree generation stayed settled across a change below it")
	}
	reconciled, err := catalog.Snapshot(t.Context(), "p", []Root{scoped})
	testutil.FailErr(t, "reconciled subtree generation", err)
	if _, ok := reconciled.Entry(scoped.ID, "c.txt"); !ok {
		t.Fatal("subtree generation missed the change below it")
	}
	if got := walks.Load(); got != 1 {
		t.Fatalf("walks = %d, want 1", got)
	}
}
