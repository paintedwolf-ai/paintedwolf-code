package scan

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/sourcesnapshot"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testutil"
)

func writeScanSource(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	testutil.FailErr(t, "mkdir "+rel, os.MkdirAll(filepath.Dir(path), 0o755))
	testutil.FailErr(t, "write "+rel, os.WriteFile(path, []byte(content), 0o644))
}

func TestDiffSourceGenerationsDerivesExactUpsertsAndDeletions(t *testing.T) {
	store := NewSQLStore(testdbfixture.Open(t, "store.db"))
	snapshots := testSnapshots(t, store)
	root := t.TempDir()
	writeScanSource(t, root, "changed.go", "old")
	writeScanSource(t, root, "deleted.go", "gone")
	writeScanSource(t, root, "same.go", "same")
	previous, err := snapshots.EnsurePath(t.Context(), root, sourcesnapshot.VerifyContent)
	testutil.FailErr(t, "publish previous", err)

	writeScanSource(t, root, "changed.go", "changed")
	writeScanSource(t, root, "added.go", "new")
	testutil.FailErr(t, "delete", os.Remove(filepath.Join(root, "deleted.go")))
	current, err := snapshots.EnsurePath(t.Context(), root, sourcesnapshot.VerifyContent)
	testutil.FailErr(t, "publish current", err)

	generation, err := DiffSourceGenerations(t.Context(), snapshots, previous, current)
	testutil.FailErr(t, "diff generations", err)
	if generation.PreviousID != previous.ID || generation.Snapshot.ID != current.ID {
		t.Fatalf("generation identity = %+v", generation)
	}
	if got := generation.UpsertedPaths; len(got) != 2 || got[0] != "added.go" || got[1] != "changed.go" {
		t.Fatalf("upserts = %#v", got)
	}
	if got := generation.DeletedPaths; len(got) != 1 || got[0] != "deleted.go" {
		t.Fatalf("deletions = %#v", got)
	}
}

func TestValidateSnapshotTargetsUsesManifestNotFilesystem(t *testing.T) {
	store := NewSQLStore(testdbfixture.Open(t, "store.db"))
	snapshots := testSnapshots(t, store)
	root := t.TempDir()
	writeScanSource(t, root, "pkg/present.go", "present")
	snapshot, err := snapshots.EnsurePath(t.Context(), root, sourcesnapshot.VerifyStat)
	testutil.FailErr(t, "publish", err)
	// The tree moves on; the manifest is what a target is validated against.
	writeScanSource(t, root, "later.go", "later")
	testutil.FailErr(t, "remove", os.Remove(filepath.Join(root, "pkg", "present.go")))

	paths, err := ValidateSnapshotTargets(t.Context(), snapshots, snapshot, []string{"pkg/present.go", "pkg"})
	testutil.FailErr(t, "validate manifest targets", err)
	if len(paths) != 2 || paths[0] != "pkg" || paths[1] != "pkg/present.go" {
		t.Fatalf("validated paths = %#v", paths)
	}
	if _, err := ValidateSnapshotTargets(t.Context(), snapshots, snapshot, []string{"later.go"}); err == nil {
		t.Fatal("a file the manifest never held was accepted")
	}
}
