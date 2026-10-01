package sourcesnapshot

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

func countRows(t *testing.T, store *Store, query string) int {
	t.Helper()
	var count int
	testutil.FailErr(t, "count rows", store.db.QueryRowContext(t.Context(), query).Scan(&count))
	return count
}

// Releasing a root drops its references; maintenance reclaims the unshared chunks.
func TestReleaseRootsGivesUpSnapshotsAndTheirManifests(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "a.txt", "hello\n")
	writeSource(t, root, "b.txt", "world\n")

	_, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	testutil.FailErr(t, "publish snapshot", err)

	if countRows(t, store, `SELECT COUNT(*) FROM source_snapshots`) == 0 {
		t.Fatal("publish recorded no snapshot")
	}
	if countRows(t, store, `SELECT COUNT(*) FROM source_manifest_entries`) == 0 {
		t.Fatal("publish recorded no manifest entries")
	}

	testutil.FailErr(t, "release roots", store.ReleaseRoots(t.Context(), []Root{{Path: root}}))
	if got := countRows(t, store, `SELECT COUNT(*) FROM source_snapshot_heads`); got != 0 {
		t.Fatalf("heads after release = %d want 0", got)
	}
	testutil.FailErr(t, "sweep released manifests", store.Sweep(t.Context(), time.Now().Add(-Retention)))

	for _, check := range []struct {
		what  string
		query string
	}{
		{"snapshots", `SELECT COUNT(*) FROM source_snapshots`},
		{"snapshot heads", `SELECT COUNT(*) FROM source_snapshot_heads`},
		{"manifest chunks", `SELECT COUNT(*) FROM source_manifest_chunks`},
		{"manifest entries", `SELECT COUNT(*) FROM source_manifest_entries`},
	} {
		if got := countRows(t, store, check.query); got != 0 {
			t.Fatalf("%s after release = %d want 0", check.what, got)
		}
	}
}

// Releasing one root leaves other roots' snapshots alone.
func TestReleaseRootsLeavesOtherRootsAlone(t *testing.T) {
	store := openSnapshotStore(t)
	kept := t.TempDir()
	gone := t.TempDir()
	writeSource(t, kept, "keep.txt", "keep\n")
	writeSource(t, gone, "drop.txt", "drop\n")

	_, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: kept}}})
	testutil.FailErr(t, "publish kept", err)
	_, err = store.Ensure(t.Context(), Request{Roots: []Root{{Path: gone}}})
	testutil.FailErr(t, "publish gone", err)

	before := countRows(t, store, `SELECT COUNT(*) FROM source_snapshots`)
	if before != 2 {
		t.Fatalf("snapshots = %d want 2", before)
	}

	testutil.FailErr(t, "release one root", store.ReleaseRoots(t.Context(), []Root{{Path: gone}}))

	if got := countRows(t, store, `SELECT COUNT(*) FROM source_snapshots`); got != 1 {
		t.Fatalf("snapshots after release = %d want 1", got)
	}
	if got := countRows(t, store, `SELECT COUNT(*) FROM source_snapshot_heads`); got != 1 {
		t.Fatalf("heads after release = %d want 1", got)
	}
}

func TestReleaseRootsIgnoresRootsItWasNeverGiven(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "a.txt", "hello\n")
	_, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	testutil.FailErr(t, "publish snapshot", err)

	testutil.FailErr(t, "release nothing", store.ReleaseRoots(t.Context(), nil))
	if got := countRows(t, store, `SELECT COUNT(*) FROM source_snapshots`); got != 1 {
		t.Fatalf("snapshots = %d want 1", got)
	}
}

// A publication holds its admitted entries in the staging table; nothing
// survives it, and a build another process abandoned is swept.
func TestPublishLeavesNoStagingBehind(t *testing.T) {
	store := openSnapshotStore(t)
	root := t.TempDir()
	writeSource(t, root, "a.txt", "hello\n")
	_, err := store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	testutil.FailErr(t, "publish snapshot", err)
	if got := countRows(t, store, `SELECT COUNT(*) FROM source_manifest_staging`); got != 0 {
		t.Fatalf("staging rows after publish = %d want 0", got)
	}
	_, err = store.db.ExecContext(t.Context(), `
		INSERT INTO source_manifest_staging (build_id, bucket, root_path, path, sha256, git_oid, identity, size, mode, modified_ns)
		VALUES ('deadbeef-1', 0, ?, 'stale.txt', '', '', 'stat', 1, 420, 0)`, root)
	testutil.FailErr(t, "plant an abandoned build", err)
	_, err = store.Ensure(t.Context(), Request{Roots: []Root{{Path: root}}})
	testutil.FailErr(t, "publish again", err)
	if got := countRows(t, store, `SELECT COUNT(*) FROM source_manifest_staging`); got != 0 {
		t.Fatalf("abandoned staging rows after sweep = %d want 0", got)
	}
}
