package db

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestRetainedCopyRejectsLossOutsideFilteredTables(t *testing.T) {
	source, _ := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 3})
	reader, err := OpenReadOnly(t.Context(), source)
	testutil.FailErr(t, "open source", err)
	plan, err := PlanUpgrade(t.Context(), reader)
	testutil.FailErr(t, "plan copy", err)
	testutil.FailErr(t, "close reader", reader.Close())
	before, err := os.ReadFile(source)
	testutil.FailErr(t, "read original", err)
	plan.CopyPlan().Sources["source_snapshots"] = `FROM original.source_snapshots e WHERE 0`
	if _, err := copyRetainedStore(t.Context(), source, filepath.Join(t.TempDir(), "copy.db"), plan, nil); err == nil {
		t.Fatal("accepted dropped durable rows")
	}
	after, err := os.ReadFile(source)
	testutil.FailErr(t, "reread original", err)
	if !bytes.Equal(before, after) {
		t.Fatal("failed copy changed original")
	}
}

func TestSkippedReleasesUseRetainedRebuild(t *testing.T) {
	for _, fixture := range []struct {
		version  string
		revision int
	}{{"1.0.0", 1}, {"1.0.2-rc1", 2}, {"1.0.3-rc1", 3}, {"1.0.3-rc1", 4}} {
		t.Run(fixture.version+string(rune('0'+fixture.revision)), func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "upgrade-corpus", fixture.version, "store.db"))
			testutil.FailErr(t, "read immutable fixture", err)
			path := filepath.Join(t.TempDir(), "store.db")
			testutil.FailErr(t, "copy fixture", os.WriteFile(path, raw, 0600))
			if fixture.revision == 3 {
				connection, err := sql.Open("sqlite", path)
				testutil.FailErr(t, "open revision-three fixture", err)
				_, err = connection.Exec(`DELETE FROM schema_migrations WHERE to_revision=4; PRAGMA user_version=3`)
				testutil.FailErr(t, "select registered same-shape revision three", err)
				testutil.FailErr(t, "close fixture", connection.Close())
			}
			journalObserved := false
			hooks := UpgradeHooks{Progress: func(migrations.Phase) {
				if _, err := os.Stat(rebuildMarker(path)); err == nil {
					journalObserved = true
				}
			}}
			testutil.FailErr(t, "upgrade skipped release", upgradeExisting(t.Context(), path, hooks, true))
			if !journalObserved {
				t.Fatal("skipped release used in-place retirement")
			}
			reader, err := OpenReadOnly(t.Context(), path)
			testutil.FailErr(t, "open migrated fixture", err)
			defer reader.Close()
			testutil.FailErr(t, "verify migrated fixture", auditSnapshot(t.Context(), reader, nil))
		})
	}
}

func TestRetainedCopyClassifiesInconsistentChunkCounts(t *testing.T) {
	source, _ := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 3})
	database, err := sql.Open("sqlite", source)
	testutil.FailErr(t, "open retained metadata", err)
	_, err = database.Exec(`UPDATE source_manifest_chunks SET entry_count=entry_count+1 WHERE id IN (SELECT chunk_id FROM source_snapshot_chunks)`)
	testutil.FailErr(t, "damage retained count", err)
	testutil.FailErr(t, "close metadata", database.Close())
	testutil.FailErr(t, "seal byte-comparison baseline", sealRebuiltStore(t.Context(), source))
	before, err := os.ReadFile(source)
	testutil.FailErr(t, "read damaged source", err)
	err = UpgradeStaged(t.Context(), source)
	var failure *StoreIncompatibleError
	if !errors.As(err, &failure) || failure.Reason != RecoveryReasonIntegrityFailed {
		t.Fatalf("inconsistent retained count classification = %v", err)
	}
	after, err := os.ReadFile(source)
	testutil.FailErr(t, "read refused source", err)
	if !bytes.Equal(before, after) {
		t.Fatal("refused copy changed original bytes")
	}
	if _, err := os.Stat(rebuildMarker(source)); !os.IsNotExist(err) {
		t.Fatalf("failed copy left publication journal: %v", err)
	}
	work, err := filepath.Glob(filepath.Join(filepath.Dir(source), StoreUpgradeDirectoryPrefix+"*"))
	testutil.FailErr(t, "inspect unpublished work", err)
	if len(work) != 0 {
		t.Fatal("failed copy left unpublished work")
	}
}
