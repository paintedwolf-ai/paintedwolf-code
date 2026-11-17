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

const revision6LedgerID = "outside-namespace"

// releasedRevision5Store copies the 1.0.3-rc2 corpus, the newest shipped shape.
func releasedRevision5Store(t *testing.T, path string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(corpusDir(), "1.0.3-rc2", "store.db"))
	testutil.FailErr(t, "read released store", err)
	testutil.FailErr(t, "copy released store", os.WriteFile(path, raw, 0o600))
}

func TestRevision6UpgradesEveryReleasedStore(t *testing.T) {
	for _, version := range []string{"1.0.0", "1.0.2", "1.0.3-rc2"} {
		t.Run(version, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(corpusDir(), version, "store.db"))
			testutil.FailErr(t, "read immutable released store", err)
			path := filepath.Join(t.TempDir(), "store.db")
			testutil.FailErr(t, "copy released store", os.WriteFile(path, raw, 0o600))
			released, err := sql.Open("sqlite", path)
			testutil.FailErr(t, "open released store", err)
			var roots int
			testutil.FailErr(t, "count released roots", released.QueryRow(`SELECT count(*) FROM project_roots`).Scan(&roots))
			testutil.FailErr(t, "close released store", released.Close())

			testutil.FailErr(t, "upgrade complete registered route", UpgradeStaged(t.Context(), path))
			testutil.FailErr(t, "reopen complete registered route", UpgradeStaged(t.Context(), path))
			upgraded, err := sql.Open("sqlite", path)
			testutil.FailErr(t, "read upgraded released store", err)
			defer upgraded.Close()
			diff, err := BaselineShapeDiff(t.Context(), upgraded)
			testutil.FailErr(t, "compare released route with complete fresh target", err)
			if len(diff) != 0 {
				t.Fatalf("released route shape differs: %v", diff)
			}
			var count int
			testutil.FailErr(t, "read migration ledger", upgraded.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id=? AND from_revision=5 AND to_revision=6`, revision6LedgerID).Scan(&count))
			if count != 1 {
				t.Fatalf("revision 6 applied %d times", count)
			}
			var folders, without int
			testutil.FailErr(t, "count folder roots", upgraded.QueryRow(`SELECT count(*) FROM project_roots WHERE kind != 'outside'`).Scan(&folders))
			if folders != roots {
				t.Fatalf("upgrade changed folder roots %d -> %d", roots, folders)
			}
			testutil.FailErr(t, "count projects without an outside namespace", upgraded.QueryRow(`SELECT count(*) FROM projects p WHERE NOT EXISTS (
				SELECT 1 FROM project_roots r WHERE r.project_id = p.id AND r.kind = 'outside' AND r.path = '' AND r.is_primary = 0)`).Scan(&without))
			if without != 0 {
				t.Fatalf("%d projects lack an outside namespace", without)
			}
		})
	}
}

func TestRevision6OutsideNamespaceLeavesFolderRulesAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	releasedRevision5Store(t, path)
	testutil.FailErr(t, "upgrade released source", UpgradeStaged(t.Context(), path))
	store, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded store", err)
	defer store.Close()
	_, err = store.Exec(`INSERT INTO projects(id,last_opened_at,created_at) VALUES('p6','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "create project", err)
	var outside string
	testutil.FailErr(t, "read new project's outside namespace", store.QueryRow(`SELECT id FROM project_roots WHERE project_id='p6' AND kind='outside'`).Scan(&outside))
	_, err = store.Exec(`INSERT INTO project_roots(id,project_id,path,label,is_primary,added_at,kind) VALUES('folder','p6','/work/outside','outside',1,'2026-01-01T00:00:00Z','attached')`)
	testutil.FailErr(t, "attach a folder labeled outside", err)
	if _, err := store.Exec(`INSERT INTO project_roots(id,project_id,path,label,is_primary,added_at,kind) VALUES('second','p6','','outside',0,'2026-01-01T00:00:00Z','outside')`); err == nil {
		t.Fatal("a project accepted a second outside namespace")
	}
	const session = `INSERT INTO sessions(id,project_id,owner_person_id,posture,workspace_root_id,created_at,activity_at,updated_at) VALUES(?,'p6',(SELECT id FROM people WHERE role='owner'),'build',?,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`
	_, err = store.Exec(session, "s6-folder", "folder")
	testutil.FailErr(t, "start a session on the folder", err)
	if _, err := store.Exec(session, "s6-outside", outside); err == nil {
		t.Fatal("a session took the outside namespace as its workspace root")
	}
}

// A store claiming revision 6 with the revision-5 shape is an unknown shape.
func TestRevision6ClaimWithoutItsShapeIsRefusedWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	releasedRevision5Store(t, path)
	source, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open released store", err)
	released, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect released store", err)
	if released != migrations.Revision6(released).From {
		t.Fatalf("released store is %+v, not the registered revision-5 source", released)
	}
	_, err = source.ExecContext(t.Context(), `PRAGMA user_version=6`)
	testutil.FailErr(t, "claim revision 6", err)
	testutil.FailErr(t, "close source", source.Close())
	before, err := os.ReadFile(path)
	testutil.FailErr(t, "read source bytes", err)
	var incompatible *StoreIncompatibleError
	if err := UpgradeStaged(t.Context(), path); !errors.As(err, &incompatible) || incompatible.Reason != RecoveryReasonSchemaMismatch || incompatible.StoreSchemaVersion != 6 {
		t.Fatalf("revision 6 claim was not refused as a schema mismatch: %v", err)
	}
	after, err := os.ReadFile(path)
	testutil.FailErr(t, "read refused bytes", err)
	if !bytes.Equal(before, after) {
		t.Fatal("refused store was mutated")
	}
}

// An upgraded store keeps a window's coverage until the window goes, and
// refuses a gap without its reason.
func TestRevision6CommandWindowCoverageFollowsItsWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	releasedRevision5Store(t, path)
	testutil.FailErr(t, "upgrade released source", UpgradeStaged(t.Context(), path))
	store, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded store", err)
	defer store.Close()
	_, err = store.Exec(`INSERT INTO projects(id,last_opened_at,created_at) VALUES('p6','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "create project", err)
	_, err = store.Exec(`INSERT INTO source_command_windows(id,project_id,session_id,tool_name,command_line,state,ordinal,started_ts) VALUES('w6','p6','s6','command','make','ended',1,'2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "create window", err)
	_, err = store.Exec(`INSERT INTO source_command_window_coverage(window_id,root_id,path,coverage,reason) VALUES('w6','outside','home/me','not_observed','time_budget')`)
	testutil.FailErr(t, "record a gap", err)
	if _, err := store.Exec(`INSERT INTO source_command_window_coverage(window_id,root_id,path,coverage) VALUES('w6','outside','srv','not_observed')`); err == nil {
		t.Fatal("a gap without a reason was accepted")
	}
	_, err = store.Exec(`DELETE FROM source_command_windows WHERE id='w6'`)
	testutil.FailErr(t, "delete window", err)
	var left int
	testutil.FailErr(t, "count coverage", store.QueryRow(`SELECT count(*) FROM source_command_window_coverage`).Scan(&left))
	if left != 0 {
		t.Fatalf("%d coverage rows outlived their window", left)
	}
}
