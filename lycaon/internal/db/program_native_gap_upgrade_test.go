package db

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

// The source is the fixed main6 schema plus the unchanged authority and receipt steps.
func receiptRevision8Store(t *testing.T, path string) *sql.DB {
	t.Helper()
	source := authorityRevision7Store(t, path)
	target := migrations.ProgramNativeGap(migrations.Baseline{}).From
	tx, err := source.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin established receipt step", err)
	step := migrations.ProgramReceipts(target)
	testutil.FailErr(t, "apply established receipt step", step.Apply(t.Context(), tx))
	_, err = tx.ExecContext(t.Context(), `PRAGMA user_version=8`)
	testutil.FailErr(t, "stamp established receipt revision", err)
	testutil.FailErr(t, "commit receipt source", tx.Commit())
	actual, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect complete receipt source identity", err)
	if actual != target {
		t.Fatalf("receipt input differs from declared prerequisite: %+v", actual)
	}
	return source
}

func TestProgramNativeGapUpgradesExactReceipt8AndRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	source := receiptRevision8Store(t, path)
	_, err := source.ExecContext(t.Context(), `
 INSERT INTO projects(id,last_opened_at,created_at) VALUES('fixture-project','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
 INSERT INTO sessions(id,project_id,owner_person_id,posture,created_at,activity_at,updated_at) VALUES('fixture-session','fixture-project','main-person','build','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');
 INSERT INTO invocation_receipts(id,project_id,session_id,tool_call_id,tool_name,owner,lifecycle,reversibility,evidence_policy,recovery_policy,evidence_kind,contract_digest,args_digest,status,started_at) VALUES('parent','fixture-project','fixture-session','actual-model-call','command','processes','effect_attempt','irreversible','attempt','abandon','pending','contract','args','running','2026-01-01T00:00:00Z');
 INSERT INTO invocation_programs(id,parent_receipt_id,root_execution_id,language_version,catalog_digest,source_unit_id,source_digest,source_ref,source_exact,plan_ref,syntax_validated,state,started_at) VALUES('program','parent','root',1,'catalog','source','digest','source-ref',1,'plan-ref',1,'running','2026-01-01T00:00:00Z');
 INSERT INTO invocation_program_sources VALUES('program','source','source-id','revision','utf8-no-bom','digest',0,3,'source-ref');
 INSERT INTO invocation_program_nodes VALUES('program','node','source',0,3,1,1,'reached');
 INSERT INTO invocation_program_definitions VALUES('program','definition','external','processes','effect_attempt','{}');
 INSERT INTO invocation_program_executions(id,program_id,node_id,ordinal,parent_execution_id,actor_id,scope_json,operation_id,definition_digest,arguments_representation,arguments_digest,arguments_ref,canonical,state,owner,lifecycle,started_at) VALUES('execution','program','node',1,'root','main-person','{}','external','definition','operation_arguments_v1','arguments','arguments-ref','external -- helper','running','processes','effect_attempt','2026-01-01T00:00:00Z');
 INSERT INTO invocation_program_revision_pins VALUES('program','execution',0,'source','published_disk','captured','root','kept.txt','',0,'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa','original-encoded-bytes','aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa',7,'2026-01-01T00:00:00Z');
 UPDATE invocation_program_executions SET reviewed_arguments_representation='operation_arguments_v1',reviewed_arguments_digest='reviewed-arguments',reviewed_arguments_ref='reviewed-ref',owner_started=1,owner_invoked=1,state='completed',status_code=0,complete=1,effect_certainty='attempted',published_revision='published-revision',detail_ref='detail-ref',settled_at='2026-01-01T00:00:01Z' WHERE id='execution';`)
	testutil.FailErr(t, "seed actual parent program execution and pin", err)
	retained := seedNativeAuthorityMainData(t, source)
	for _, table := range []string{"invocation_receipts", "invocation_programs", "invocation_program_sources", "invocation_program_nodes", "invocation_program_definitions", "invocation_program_revision_pins"} {
		retained[table] = namespaceTableContents(t, source, table)
	}
	columns, err := readTableColumns(t.Context(), source, "invocation_program_executions")
	testutil.FailErr(t, "read all preexisting execution columns", err)
	selection := make([]string, len(columns))
	for i, column := range columns {
		selection[i] = `"` + column.Name + `"`
	}
	executionValues := func(database *sql.DB) []any {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		testutil.FailErr(t, "read every original execution value", database.QueryRowContext(t.Context(),
			`SELECT `+strings.Join(selection, ",")+` FROM invocation_program_executions WHERE id='execution'`).Scan(targets...))
		return values
	}
	beforeExecution := executionValues(source)
	testutil.FailErr(t, "close receipt8 before registered upgrade", source.Close())
	testutil.FailErr(t, "upgrade complete registered receipt8 route", UpgradeStaged(t.Context(), path))
	testutil.FailErr(t, "reopen complete registered receipt8 route", UpgradeStaged(t.Context(), path))
	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "open upgraded native-gap target", err)
	defer upgraded.Close()
	fresh, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fresh.db"))
	testutil.FailErr(t, "open independent fresh9 target", err)
	defer fresh.Close()
	testutil.FailErr(t, "install complete fresh9 schema", execFreshSchema(t.Context(), fresh))
	want, err := ReadShape(t.Context(), fresh)
	testutil.FailErr(t, "read complete fresh9 shape", err)
	got, err := ReadShape(t.Context(), upgraded)
	testutil.FailErr(t, "read complete upgraded shape", err)
	if diff := want.Diff(got); len(diff) != 0 {
		t.Fatalf("native-gap upgrade differs from complete fresh target: %v", diff)
	}
	for table, before := range retained {
		if after := namespaceTableContents(t, upgraded, table); after != before {
			t.Fatalf("native-gap upgrade changed retained %s data", table)
		}
	}
	if afterExecution := executionValues(upgraded); !reflect.DeepEqual(beforeExecution, afterExecution) {
		t.Fatalf("native-gap upgrade changed preexisting execution columns %v: before=%v after=%v", selection, beforeExecution, afterExecution)
	}
	var command, gap string
	testutil.FailErr(t, "read retained execution and default gap", upgraded.QueryRow(`SELECT canonical,native_gap_json FROM invocation_program_executions WHERE id='execution'`).Scan(&command, &gap))
	if command != "external -- helper" || gap != "" {
		t.Fatalf("upgraded execution = %q, gap = %q", command, gap)
	}
	const recorded = `{"command":"sed","form":"--unsupported"}`
	_, err = upgraded.Exec(`UPDATE invocation_program_executions SET native_gap_json=? WHERE id='execution'`, recorded)
	testutil.FailErr(t, "record native gap after upgrade", err)
	if _, err := upgraded.Exec(`UPDATE invocation_program_executions SET native_gap_json='invalid' WHERE id='execution'`); err == nil {
		t.Fatal("native-gap storage accepted invalid JSON")
	}
	testutil.FailErr(t, "read retained native gap", upgraded.QueryRow(`SELECT native_gap_json FROM invocation_program_executions WHERE id='execution'`).Scan(&gap))
	if gap != recorded {
		t.Fatalf("native gap changed: %q", gap)
	}
	var count int
	testutil.FailErr(t, "read native-gap migration ledger", upgraded.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id='portable-program-native-gap'`).Scan(&count))
	if count != 1 {
		t.Fatalf("native-gap step applied %d times", count)
	}
	actual, err := inspectSchema(t.Context(), upgraded)
	testutil.FailErr(t, "inspect fully compared native-gap target", err)
	t.Logf("verified complete receipt8-to-native-gap shape: source=%+v target=%+v", migrations.ProgramNativeGap(actual).From, actual)
}

func TestProgramNativeGapRefusesUnknownRevision9WithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unknown.db")
	source := receiptRevision8Store(t, path)
	_, err := source.Exec(`PRAGMA user_version=9`)
	testutil.FailErr(t, "claim revision9 without native-gap column", err)
	testutil.FailErr(t, "close unknown source", source.Close())
	before, err := os.ReadFile(path)
	testutil.FailErr(t, "read unknown source bytes", err)
	var incompatible *StoreIncompatibleError
	if err := UpgradeStaged(t.Context(), path); !errors.As(err, &incompatible) || incompatible.Reason != RecoveryReasonSchemaMismatch {
		t.Fatalf("unknown native-gap shape was not refused: %v", err)
	}
	after, err := os.ReadFile(path)
	testutil.FailErr(t, "read refused source bytes", err)
	if !bytes.Equal(before, after) {
		t.Fatal("refused native-gap source was mutated")
	}
}
