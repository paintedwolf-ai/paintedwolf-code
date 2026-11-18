package db

import (
	"database/sql"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func assertOutsideUpgrade(t *testing.T, upgraded *sql.DB, roots int) {
	t.Helper()
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
}

func TestOutsideNamespaceLeavesFolderRulesAlone(t *testing.T) {
	path := copyReleasedStore(t, "1.0.2")
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
