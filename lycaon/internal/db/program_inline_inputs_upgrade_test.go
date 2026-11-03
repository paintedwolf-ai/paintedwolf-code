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
	target, err := CurrentBaseline(t.Context())
	testutil.FailErr(t, "inspect target", err)
	source := nativeGapRevision9Store(t, path, migrations.ProgramInlineInputs(target).From)
	_, err = source.ExecContext(t.Context(), `
		INSERT INTO projects(id,last_opened_at,created_at) VALUES('inline-project','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO sessions(id,project_id,owner_person_id,posture,created_at,activity_at,updated_at) VALUES('inline-session','inline-project','main-person','build','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
		INSERT INTO invocation_receipts(id,project_id,session_id,tool_call_id,tool_name,owner,lifecycle,reversibility,evidence_policy,recovery_policy,evidence_kind,contract_digest,args_digest,status,started_at) VALUES('inline-parent','inline-project','inline-session','inline-call','command','processes','effect_attempt','irreversible','attempt','abandon','pending','contract','args','running','2026-01-01T00:00:00Z');
		INSERT INTO invocation_programs(id,parent_receipt_id,root_execution_id,language_version,catalog_digest,source_unit_id,source_digest,source_ref,source_exact,plan_ref,syntax_validated,state,started_at) VALUES('inline-program','inline-parent','inline-root',1,'catalog','submitted','source','source-ref',1,'plan-ref',1,'running','2026-01-01T00:00:00Z');
		INSERT INTO invocation_program_outputs(id,program_id,stream,reference,retention_claim_id,initiating_claim_id,blob_id,blob_json,complete,redacted,mappings_json,created_at) VALUES('old-output','inline-program','stdout','old-reference','old-claim','old-initiating','old-blob','{"ID":"old-blob"}',1,0,'[]','2026-01-01T00:00:00Z');`)
	testutil.FailErr(t, "seed retained blob receipt", err)
	intermediate, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect revision 9", err)
	if intermediate != migrations.ProgramInlineInputs(target).From {
		t.Fatalf("revision 9 shape=%+v", intermediate)
	}
	testutil.FailErr(t, "close source", source.Close())
	testutil.FailErr(t, "upgrade revision 9", UpgradeStaged(t.Context(), path))
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

// nativeGapRevision9Store is the receipt 8 source plus the unchanged native gap
// step, stamped as revision 9 and checked against the declared prerequisite.
func nativeGapRevision9Store(t *testing.T, path string, target migrations.Baseline) *sql.DB {
	t.Helper()
	source := receiptRevision8Store(t, path)
	tx, err := source.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin established native gap step", err)
	testutil.FailErr(t, "apply established native gap step", migrations.ProgramNativeGap(target).Apply(t.Context(), tx))
	_, err = tx.ExecContext(t.Context(), `PRAGMA user_version=9`)
	testutil.FailErr(t, "stamp established native gap revision", err)
	testutil.FailErr(t, "commit native gap source", tx.Commit())
	actual, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect complete native gap source identity", err)
	if actual != target {
		t.Fatalf("native gap input differs from declared prerequisite: %+v", actual)
	}
	return source
}
