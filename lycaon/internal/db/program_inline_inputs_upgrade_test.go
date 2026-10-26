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
	DeriveRevision5Fixture(t, source)
	target, err := CurrentBaseline(t.Context())
	testutil.FailErr(t, "inspect target", err)
	step := migrations.ProgramReceipts(migrations.ProgramInlineInputs(target).From)
	tx, err := source.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin revision 6 fixture", err)
	testutil.FailErr(t, "install revision 6 receipt schema", step.Apply(t.Context(), tx))
	_, err = tx.ExecContext(t.Context(), `PRAGMA user_version=6`)
	testutil.FailErr(t, "mark revision 6 fixture", err)
	_, err = tx.ExecContext(t.Context(), `INSERT INTO schema_migrations(id,checksum,from_revision,to_revision,applied_at) VALUES(?,?,5,6,'2026-01-01T00:00:00Z')`, step.ID, step.Checksum)
	testutil.FailErr(t, "record revision 6 fixture", err)
	testutil.FailErr(t, "commit revision 6 fixture", tx.Commit())
	_, err = source.ExecContext(t.Context(), `
		INSERT INTO projects(id,last_opened_at,created_at) VALUES('inline-project','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO sessions(id,project_id,owner_person_id,posture,created_at,activity_at,updated_at) VALUES('inline-session','inline-project',(SELECT id FROM people WHERE role='owner'),'build','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO invocation_receipts(id,project_id,session_id,tool_call_id,tool_name,owner,lifecycle,reversibility,evidence_policy,recovery_policy,evidence_kind,contract_digest,args_digest,status,started_at) VALUES('inline-parent','inline-project','inline-session','inline-call','command','processes','effect_attempt','irreversible','attempt','abandon','pending','contract','args','running','2026-01-01T00:00:00Z');
		INSERT INTO invocation_programs(id,parent_receipt_id,root_execution_id,language_version,catalog_digest,source_unit_id,source_digest,source_ref,source_exact,plan_ref,syntax_validated,state,started_at) VALUES('inline-program','inline-parent','inline-root',1,'catalog','submitted','source','source-ref',1,'plan-ref',1,'running','2026-01-01T00:00:00Z');
		INSERT INTO invocation_program_outputs(id,program_id,stream,reference,retention_claim_id,initiating_claim_id,blob_id,blob_json,complete,redacted,mappings_json,created_at) VALUES('old-output','inline-program','stdout','old-reference','old-claim','old-initiating','old-blob','{"ID":"old-blob"}',1,0,'[]','2026-01-01T00:00:00Z');`)
	testutil.FailErr(t, "seed retained blob receipt", err)
	intermediate, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect revision 6", err)
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
	var blobID sql.NullString
	testutil.FailErr(t, "preserve indexed blob identity", upgraded.QueryRow(`SELECT blob_id FROM invocation_program_outputs WHERE id='old-output'`).Scan(&blobID))
	if !blobID.Valid || blobID.String != "old-blob" {
		t.Fatalf("upgraded blob identity=%+v", blobID)
	}
	var count int
	testutil.FailErr(t, "inspect inline migration ledger", upgraded.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id='portable-program-inline-inputs'`).Scan(&count))
	if count != 1 {
		t.Fatalf("inline migration recorded %d times", count)
	}
}
