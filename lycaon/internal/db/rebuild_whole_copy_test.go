package db

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

func completeRetainedFixture(t *testing.T) (string, migrations.Plan) {
	t.Helper()
	path, _ := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 300, "/work/b": 9})
	writer, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open complete source", err)
	_, err = writer.Exec(`DELETE FROM source_manifest_entries WHERE chunk_id='orphan'; DELETE FROM source_manifest_chunks WHERE id='orphan'; DELETE FROM source_manifest_staging`)
	testutil.FailErr(t, "retain every source row", err)
	plan, err := PlanUpgrade(t.Context(), writer)
	testutil.FailErr(t, "plan complete source", err)
	plan, err = plan.PrepareRetainedStorage(t.Context(), writer)
	testutil.FailErr(t, "measure complete source", err)
	var actual uint64
	testutil.FailErr(t, "count primary rows", writer.QueryRow("SELECT COUNT(*) FROM source_manifest_entries NOT INDEXED").Scan(&actual))
	measured, ok := plan.RetainedSourceEntries()
	if !ok || measured != actual {
		t.Fatalf("physical entry count=%d measured=%v actual=%d", measured, ok, actual)
	}
	testutil.FailErr(t, "close complete source", writer.Close())
	testutil.FailErr(t, "seal complete source", sealRebuiltStore(t.Context(), path))
	return path, plan
}

func TestRetainedCompleteSourceUsesWholeFileAndCarriesSharing(t *testing.T) {
	source, plan := completeRetainedFixture(t)
	before, err := os.ReadFile(source)
	testutil.FailErr(t, "read complete original", err)
	destination := filepath.Join(t.TempDir(), "replacement.db")
	prepared, err := copyRetainedStore(t.Context(), source, destination, plan, nil)
	testutil.FailErr(t, "prepare complete whole-file image", err)
	if !prepared.wholeFile || prepared.preparedIdentity == "" {
		t.Fatal("complete selection reconstructed rows or lost prepared-image custody")
	}
	after, err := os.ReadFile(source)
	testutil.FailErr(t, "read retained original", err)
	if !bytes.Equal(before, after) {
		t.Fatal("private whole-file preparation changed original")
	}
	reader, err := openReader(t.Context(), destination)
	testutil.FailErr(t, "open private image", err)
	storage, err := InspectUpgradeStorage(t.Context(), reader, destination, true, prepared.sharedOriginal)
	testutil.FailErr(t, "account staged sharing", err)
	if (storage.CloneBytes != 0) != prepared.sharedOriginal {
		t.Fatal("staged clone lost original divergence reserve")
	}
	testutil.FailErr(t, "close private image", reader.Close())
	captured := false
	hooks := UpgradeHooks{Before: func(_ context.Context, source UpgradeSource, _ migrations.Plan) error {
		captured = true
		if source.SharedOriginal != prepared.sharedOriginal {
			t.Fatal("recovery lost clone provenance")
		}
		return nil
	}}
	testutil.FailErr(t, "capture same prepared image", captureRetainedRecovery(t.Context(), destination, plan, hooks, false, prepared))
	if !captured {
		t.Fatal("prepared image did not reach recovery")
	}
	// Preparation custody applies to one file identity, not a reusable boolean.
	raw, err := os.ReadFile(destination)
	testutil.FailErr(t, "read prepared image", err)
	testutil.FailErr(t, "move prepared image", os.Rename(destination, destination+".saved"))
	testutil.FailErr(t, "replace with identical different inode", os.WriteFile(destination, raw, 0600))
	captured = false
	if err := captureRetainedRecovery(t.Context(), destination, plan, hooks, false, prepared); err == nil || captured {
		t.Fatal("replacement reused another file's preparation")
	}
}

func TestRetainedIncompleteSelectionKeepsRowReconstruction(t *testing.T) {
	source, _ := seedLegacyManifestStore(t, t.TempDir(), map[string]int{"/work/a": 3})
	reader, err := openReader(t.Context(), source)
	testutil.FailErr(t, "open incomplete source", err)
	plan, err := PlanUpgrade(t.Context(), reader)
	testutil.FailErr(t, "plan incomplete source", err)
	plan, err = plan.PrepareRetainedStorage(t.Context(), reader)
	testutil.FailErr(t, "measure incomplete source", err)
	testutil.FailErr(t, "close incomplete source", reader.Close())
	prepared, err := copyRetainedStore(t.Context(), source, filepath.Join(t.TempDir(), "replacement.db"), plan, nil)
	testutil.FailErr(t, "retain selected rows", err)
	if prepared.wholeFile || prepared.sharedOriginal || prepared.preparedIdentity != "" {
		t.Fatal("incomplete retention bypassed row reconstruction")
	}
}

func TestRetainedCompleteMalformedCountsStillRefuse(t *testing.T) {
	source, _ := completeRetainedFixture(t)
	writer, err := sql.Open("sqlite", source)
	testutil.FailErr(t, "open malformed fixture", err)
	_, err = writer.Exec("UPDATE source_manifest_chunks SET entry_count=entry_count+1")
	testutil.FailErr(t, "break declared counts", err)
	plan, err := PlanUpgrade(t.Context(), writer)
	testutil.FailErr(t, "plan malformed counts", err)
	plan, err = plan.PrepareRetainedStorage(t.Context(), writer)
	testutil.FailErr(t, "measure primary rows", err)
	testutil.FailErr(t, "close malformed fixture", writer.Close())
	_, err = copyRetainedStore(t.Context(), source, filepath.Join(t.TempDir(), "replacement.db"), plan, nil)
	if !errors.Is(err, migrations.ErrIntegrity) {
		t.Fatalf("bad source counts were admitted: %v", err)
	}
}

func TestRetainedCompleteCopyDefersObsoleteSecondaryIndexAudit(t *testing.T) {
	source, _ := completeRetainedFixture(t)
	reader, err := openReader(t.Context(), source)
	testutil.FailErr(t, "open complete source", err)
	var page, size int64
	testutil.FailErr(t, "locate secondary index", reader.QueryRow("SELECT rootpage FROM sqlite_schema WHERE name='idx_source_manifest_entries_path'").Scan(&page))
	testutil.FailErr(t, "read page size", reader.QueryRow("PRAGMA page_size").Scan(&size))
	testutil.FailErr(t, "close source", reader.Close())
	file, err := os.OpenFile(source, os.O_RDWR, 0)
	testutil.FailErr(t, "open index page", err)
	_, err = file.WriteAt(make([]byte, size), (page-1)*size)
	testutil.FailErr(t, "damage secondary page", err)
	testutil.FailErr(t, "close index bytes", file.Close())
	reader, err = openReader(t.Context(), source)
	testutil.FailErr(t, "open damaged source", err)
	plan, err := PlanUpgrade(t.Context(), reader)
	testutil.FailErr(t, "plan damaged source", err)
	plan, err = plan.PrepareRetainedStorage(t.Context(), reader)
	testutil.FailErr(t, "measure damaged source", err)
	testutil.FailErr(t, "close damaged source", reader.Close())
	destination := filepath.Join(t.TempDir(), "replacement.db")
	prepared, err := copyRetainedStore(t.Context(), source, destination, plan, nil)
	testutil.FailErr(t, "preserve damaged source bytes for migration", err)
	if !prepared.wholeFile || prepared.preparedIdentity == "" {
		t.Fatal("complete source paid for an exhaustive pre-capture index audit")
	}
	reader, err = openReader(t.Context(), destination)
	testutil.FailErr(t, "open rebuilt image", err)
	defer reader.Close()
	if err := auditSnapshot(t.Context(), reader, nil); err == nil {
		t.Fatal("explicit exhaustive audit did not detect damaged secondary index")
	}
	pending, err := integrityAuditState(t.Context(), reader)
	testutil.FailErr(t, "read pending audit obligation", err)
	if pending != integrityAuditPendingValue {
		t.Fatal("preparation lost deferred audit obligation")
	}
}

func TestRetainedCompletenessPredicatesAreIndependent(t *testing.T) {
	for _, change := range []struct {
		name, sql string
		refused   bool
	}{
		{name: "staging", sql: `INSERT INTO source_manifest_staging(build_id,bucket,root_path,path,identity,size,mode) VALUES('pending',0,'/work/a','pending','stat',1,420)`},
		{name: "unreferenced chunk", sql: `INSERT INTO source_manifest_chunks(id,entry_count,total_bytes) VALUES('unreferenced',1,1); INSERT INTO source_manifest_entries(chunk_id,ordinal,root_path,path,identity,size,mode) VALUES('unreferenced',0,'/work/a','unreferenced','stat',1,420)`},
		{name: "incorrect counts", sql: `UPDATE source_manifest_chunks SET entry_count=entry_count+1 WHERE id=(SELECT id FROM source_manifest_chunks LIMIT 1)`, refused: true},
	} {
		t.Run(change.name, func(t *testing.T) {
			source, _ := completeRetainedFixture(t)
			writer, err := sql.Open("sqlite", source)
			testutil.FailErr(t, "open independent selection change", err)
			_, err = writer.Exec(change.sql)
			testutil.FailErr(t, "change one completeness fact", err)
			plan, err := PlanUpgrade(t.Context(), writer)
			testutil.FailErr(t, "plan changed source", err)
			plan, err = plan.PrepareRetainedStorage(t.Context(), writer)
			testutil.FailErr(t, "measure changed primary rows", err)
			testutil.FailErr(t, "close changed source", writer.Close())
			before, err := os.ReadFile(source)
			testutil.FailErr(t, "retain changed source bytes", err)
			prepared, err := copyRetainedStore(t.Context(), source, filepath.Join(t.TempDir(), "replacement.db"), plan, nil)
			if change.refused {
				if !errors.Is(err, migrations.ErrIntegrity) {
					t.Fatalf("malformed counts were accepted: %v", err)
				}
			} else {
				testutil.FailErr(t, "reconstruct incomplete source", err)
			}
			if prepared.wholeFile {
				t.Fatal("one incomplete fact bypassed selected-row reconstruction")
			}
			after, err := os.ReadFile(source)
			testutil.FailErr(t, "read original after selection", err)
			if !bytes.Equal(before, after) {
				t.Fatal("selection/refusal changed source bytes")
			}
		})
	}
}
