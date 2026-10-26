package db

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestProgramInlineInputsUpgradeAndFreshShape(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	source, err := Open(path)
	testutil.FailErr(t, "open source database", err)
	_, err = source.ExecContext(t.Context(), `ALTER TABLE invocation_program_outputs DROP COLUMN inline_body; ALTER TABLE invocation_program_outputs DROP COLUMN inline_digest; PRAGMA user_version=6;`)
	testutil.FailErr(t, "derive known revision 6", err)
	intermediate, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect revision 6", err)
	target, err := CurrentBaseline(t.Context())
	testutil.FailErr(t, "inspect target", err)
	if intermediate != migrations.ProgramInlineInputs(target).From {
		t.Fatalf("revision 6 shape=%+v", intermediate)
	}
	testutil.FailErr(t, "close source", source.Close())
	testutil.FailErr(t, "upgrade revision 6", UpgradeStaged(t.Context(), path))
	testutil.FailErr(t, "restart upgraded store", UpgradeStaged(t.Context(), path))
	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded store", err)
	defer upgraded.Close()
	actual, err := inspectSchema(t.Context(), upgraded)
	testutil.FailErr(t, "inspect upgraded shape", err)
	if actual != target {
		t.Fatalf("upgraded shape=%+v want %+v", actual, target)
	}
	var count int
	testutil.FailErr(t, "inspect inline migration ledger", upgraded.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id='portable-program-inline-inputs'`).Scan(&count))
	if count != 1 {
		t.Fatalf("inline migration recorded %d times", count)
	}
}
