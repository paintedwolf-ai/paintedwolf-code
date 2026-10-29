package db

import (
	"database/sql"
	"path/filepath"
	"testing"

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
