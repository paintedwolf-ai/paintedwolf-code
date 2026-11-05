package db

import (
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestSourceLinkKindUpgradesRevision10AndRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	target, err := CurrentBaseline(t.Context())
	testutil.FailErr(t, "inspect target", err)
	ten := migrations.SourceLinkKind(target).From
	source := nativeGapRevision9Store(t, path, migrations.ProgramInlineInputs(ten).From)
	tx, err := source.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin established inline step", err)
	testutil.FailErr(t, "apply established inline step", migrations.ProgramInlineInputs(ten).Apply(t.Context(), tx))
	_, err = tx.ExecContext(t.Context(), `PRAGMA user_version=10`)
	testutil.FailErr(t, "stamp revision 10", err)
	testutil.FailErr(t, "commit revision 10", tx.Commit())
	actual, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect revision 10", err)
	if actual != ten {
		t.Fatalf("revision 10 source differs from the declared prerequisite: %+v", actual)
	}
	_, err = source.ExecContext(t.Context(), `
		INSERT INTO projects(id,last_opened_at,created_at) VALUES('link-project','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO source_files(id,project_id,entry_kind,created_ts) VALUES('kept-file','link-project','file','2026-01-01T00:00:00Z');`)
	testutil.FailErr(t, "seed source identity", err)
	if _, err := source.ExecContext(t.Context(), `INSERT INTO source_files(id,project_id,entry_kind,created_ts) VALUES('early-link','link-project','link','2026-01-01T00:00:00Z')`); err == nil {
		t.Fatal("revision 10 accepted a link entry")
	}
	testutil.FailErr(t, "close source", source.Close())
	testutil.FailErr(t, "upgrade revision 10", UpgradeStaged(t.Context(), path))
	testutil.FailErr(t, "restart upgraded store", UpgradeStaged(t.Context(), path))
	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded store", err)
	defer upgraded.Close()
	shape, err := inspectSchema(t.Context(), upgraded)
	testutil.FailErr(t, "inspect upgraded shape", err)
	if shape != target {
		t.Fatalf("upgraded shape=%+v want %+v", shape, target)
	}
	var kind string
	testutil.FailErr(t, "read retained source identity", upgraded.QueryRow(`SELECT entry_kind FROM source_files WHERE id='kept-file'`).Scan(&kind))
	if kind != "file" {
		t.Fatalf("retained entry kind = %q", kind)
	}
	_, err = upgraded.Exec(`INSERT INTO source_files(id,project_id,entry_kind,created_ts) VALUES('new-link','link-project','link','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "record link entry after upgrade", err)
	var steps int
	testutil.FailErr(t, "inspect link step ledger", upgraded.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id='portable-source-link-kind' AND from_revision=10 AND to_revision=11`).Scan(&steps))
	if steps != 1 {
		t.Fatalf("link step recorded %d times", steps)
	}
}

func TestSessionProgressIdentityUpgradesRevision11AndRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	target, err := CurrentBaseline(t.Context())
	testutil.FailErr(t, "inspect target", err)
	eleven := migrations.SessionProgressIdentity(target).From
	ten := migrations.SourceLinkKind(eleven).From
	source := nativeGapRevision9Store(t, path, migrations.ProgramInlineInputs(ten).From)
	tx, err := source.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin established inline step", err)
	testutil.FailErr(t, "apply established inline step", migrations.ProgramInlineInputs(ten).Apply(t.Context(), tx))
	testutil.FailErr(t, "apply established link step", migrations.SourceLinkKind(eleven).Apply(t.Context(), tx))
	_, err = tx.ExecContext(t.Context(), `PRAGMA user_version=11`)
	testutil.FailErr(t, "stamp revision 11", err)
	testutil.FailErr(t, "commit revision 11", tx.Commit())
	actual, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect revision 11", err)
	if actual != eleven {
		t.Fatalf("revision 11 source differs from the declared prerequisite: %+v", actual)
	}
	_, err = source.ExecContext(t.Context(), `
		INSERT INTO projects(id,last_opened_at,created_at) VALUES('progress-project','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO sessions(id,project_id,owner_person_id,posture,created_at,activity_at,updated_at) VALUES('sess-checklist','progress-project','main-person','build','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO sessions(id,project_id,owner_person_id,posture,created_at,activity_at,updated_at) VALUES('sess-empty','progress-project','main-person','build','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO session_progress(session_id,workflow_run_id,content,updated_at) VALUES('sess-checklist','run-1',char(35,32)||'Goal'||char(10,10)||'Ship'||char(10,10,35,35,32)||'Progress'||char(10)||'- [x] First'||char(10)||'- [ ] Second'||char(10)||'- [~] Third','2026-01-01T00:00:00Z');
		INSERT INTO session_progress(session_id,workflow_run_id,content,updated_at) VALUES('sess-empty','run-1',char(35,32)||'Goal'||char(10,10)||'Empty'||char(10,10,35,35,32)||'Progress'||char(10),'2026-01-01T00:00:00Z');`)
	testutil.FailErr(t, "seed revision 11 progress rows", err)
	testutil.FailErr(t, "close source", source.Close())
	testutil.FailErr(t, "upgrade revision 11", UpgradeStaged(t.Context(), path))
	testutil.FailErr(t, "restart upgraded store", UpgradeStaged(t.Context(), path))
	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded store", err)
	defer upgraded.Close()
	shape, err := inspectSchema(t.Context(), upgraded)
	testutil.FailErr(t, "inspect upgraded shape", err)
	if shape != target {
		t.Fatalf("upgraded shape=%+v want %+v", shape, target)
	}
	var itemIDsRaw string
	var docVer int64
	testutil.FailErr(t, "read backfilled checklist row", upgraded.QueryRow(`SELECT item_ids_json, document_version FROM session_progress WHERE session_id='sess-checklist'`).Scan(&itemIDsRaw, &docVer))
	var ids []string
	testutil.FailErr(t, "decode backfilled item_ids_json", json.Unmarshal([]byte(itemIDsRaw), &ids))
	if len(ids) != 3 || docVer != 1 {
		t.Fatalf("backfilled ids=%v docVer=%d, want 3 ids and version 1", ids, docVer)
	}
	seen := map[string]struct{}{}
	for _, id := range ids {
		if _, err := uuid.Parse(id); err != nil {
			t.Fatalf("backfilled id %q is not a UUID: %v", id, err)
		}
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate backfilled id %q", id)
		}
		seen[id] = struct{}{}
	}
	testutil.FailErr(t, "read empty checklist row", upgraded.QueryRow(`SELECT item_ids_json, document_version FROM session_progress WHERE session_id='sess-empty'`).Scan(&itemIDsRaw, &docVer))
	if itemIDsRaw != "[]" || docVer != 1 {
		t.Fatalf("empty checklist item_ids_json=%q docVer=%d, want [] and 1", itemIDsRaw, docVer)
	}
	var steps int
	testutil.FailErr(t, "inspect progress identity step ledger", upgraded.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id='session-progress-item-identity' AND from_revision=11 AND to_revision=12`).Scan(&steps))
	if steps != 1 {
		t.Fatalf("progress identity step recorded %d times", steps)
	}
}
