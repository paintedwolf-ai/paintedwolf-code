package conformance

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs/privategit"
	"github.com/lycaon/lycaon/internal/testutil/gittest"
)

// newBehaviorRepo creates a small git repository with one commit of distinct content.
func newBehaviorRepo(t *testing.T, dir, marker string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte("content "+marker+"\n"), 0o644); err != nil {
		t.Fatalf("write data.txt: %v", err)
	}
	gittest.InitCommit(t, dir, "commit "+marker)
	return gittest.Run(t, dir, "rev-parse", "HEAD")
}

// importRepo drives one full store import of a repository's objects and returns the
// resulting store metrics snapshot.
func importRepo(t *testing.T, store *privategit.RetainedObjectStore, repoDir, tip string) privategit.StoreMetrics {
	t.Helper()
	if _, _, err := store.ImportRepositoryObjects(
		context.Background(),
		[]string{filepath.Join(repoDir, ".git", "objects")},
		[]string{tip},
	); err != nil {
		t.Fatalf("ImportRepositoryObjects(%s): %v", repoDir, err)
	}
	return store.Metrics()
}

// TestRetainedDeltasFreshStoreZero proves a freshly created store reports zero
// import/reuse activity, and that such a snapshot passes AssertPreparationBounds (#1667).
func TestRetainedDeltasFreshStoreZero(t *testing.T) {
	store, err := privategit.NewRetainedObjectStore(filepath.Join(t.TempDir(), "store"))
	if err != nil {
		t.Fatalf("NewRetainedObjectStore: %v", err)
	}
	defer store.Close()

	snap := SnapshotCoreCounters(nil, store, 0, 0, 0, 0)
	if snap.RetainedImports != 0 || snap.RetainedReuses != 0 {
		t.Fatalf("fresh store snapshot: imports=%d reuses=%d; want 0/0", snap.RetainedImports, snap.RetainedReuses)
	}
	if err := AssertPreparationBounds(snap); err != nil {
		t.Fatalf("AssertPreparationBounds on fresh store: %v", err)
	}
}

// TestRetainedDeltasPreexistingStoreBaseline proves a preexisting (nonempty) store starts
// from a nonzero baseline and that deltas between snapshots isolate only the new activity
// window (#1667). The delta — not the absolute value — is the assertion unit.
func TestRetainedDeltasPreexistingStoreBaseline(t *testing.T) {
	tmp := t.TempDir()
	repoA := filepath.Join(tmp, "repo-a")
	tipA := newBehaviorRepo(t, repoA, "alpha")

	store, err := privategit.NewRetainedObjectStore(filepath.Join(tmp, "store"))
	if err != nil {
		t.Fatalf("NewRetainedObjectStore: %v", err)
	}
	defer store.Close()

	// Populate the store: it is now preexisting/nonempty.
	if m := importRepo(t, store, repoA, tipA); m.ObjectsImported == 0 {
		t.Fatalf("first import imported 0 objects; metrics = %+v", m)
	}
	baseline := SnapshotCoreCounters(nil, store, 0, 0, 0, 0)
	if baseline.RetainedImports == 0 {
		t.Fatalf("preexisting store baseline imports = 0; want > 0")
	}

	// New activity: import a second, distinct repository.
	repoB := filepath.Join(tmp, "repo-b")
	tipB := newBehaviorRepo(t, repoB, "beta")
	importRepo(t, store, repoB, tipB)
	after := SnapshotCoreCounters(nil, store, 0, 0, 0, 0)

	delta := DiffCounters(baseline, after)
	if delta.RetainedImports <= 0 {
		t.Fatalf("delta imports = %d; want > 0 for distinct-repo import", delta.RetainedImports)
	}
	if delta.RetainedReuses != 0 {
		t.Fatalf("delta reuses = %d; want 0 for distinct-repo import", delta.RetainedReuses)
	}
}

// TestRetainedDeltasRepeatedReuse proves that re-importing the same objects counts as
// reuse activity, not import activity: the reuse delta is positive and the import delta
// is zero across the repeated-import window (#1667).
func TestRetainedDeltasRepeatedReuse(t *testing.T) {
	tmp := t.TempDir()
	repoA := filepath.Join(tmp, "repo-a")
	tipA := newBehaviorRepo(t, repoA, "alpha")

	store, err := privategit.NewRetainedObjectStore(filepath.Join(tmp, "store"))
	if err != nil {
		t.Fatalf("NewRetainedObjectStore: %v", err)
	}
	defer store.Close()

	if m := importRepo(t, store, repoA, tipA); m.ObjectsImported == 0 {
		t.Fatalf("first import imported 0 objects; metrics = %+v", m)
	}
	before := SnapshotCoreCounters(nil, store, 0, 0, 0, 0)

	// Repeated import of the SAME repository: every object already exists in the store.
	importRepo(t, store, repoA, tipA)
	after := SnapshotCoreCounters(nil, store, 0, 0, 0, 0)

	delta := DiffCounters(before, after)
	if delta.RetainedReuses <= 0 {
		t.Fatalf("delta reuses = %d; want > 0 for repeated import of same objects", delta.RetainedReuses)
	}
	if delta.RetainedImports != 0 {
		t.Fatalf("delta imports = %d; want 0 for repeated import of same objects", delta.RetainedImports)
	}
}