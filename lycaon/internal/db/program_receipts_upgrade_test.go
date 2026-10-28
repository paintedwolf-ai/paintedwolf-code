package db

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/testutil"
)

// DeriveRevision5Fixture derives the known unshipped revision5 intermediate
// from the fresh target. It adds no released baseline and does not weaken
// production shape recognition.
func DeriveRevision5Fixture(t *testing.T, source DBTX) {
	t.Helper()
	var database *sql.DB
	switch typed := source.(type) {
	case *Store:
		database = typed.writerDB()
	case *sql.DB:
		database = typed
	default:
		t.Fatalf("unsupported revision5 fixture source %T", source)
	}
	conn, err := database.Conn(t.Context())
	testutil.FailErr(t, "open fixture connection", err)
	_, err = conn.ExecContext(t.Context(), `PRAGMA foreign_keys=OFF`)
	testutil.FailErr(t, "prepare fixture source tables", err)
	tx, err := conn.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin fixture downgrade", err)
	defer tx.Rollback()
	triggers := suspendSourceFixtureTriggers(t, tx)
	views := suspendSourceFixtureViews(t, tx)
	for _, table := range []string{"source_files", "source_versions", "source_head_entries", "source_effects"} {
		downgradeLinkTableFixture(t, tx, table)
	}
	for _, definition := range append(views, triggers...) {
		_, err := tx.ExecContext(t.Context(), definition)
		testutil.FailErr(t, "restore fixture source trigger", err)
	}
	testutil.FailErr(t, "commit fixture source tables", tx.Commit())
	_, err = conn.ExecContext(t.Context(), `PRAGMA foreign_keys=ON`)
	testutil.FailErr(t, "restore fixture foreign keys", err)
	testutil.FailErr(t, "close fixture connection", conn.Close())
	_, err = source.ExecContext(t.Context(), `DROP TABLE invocation_program_revision_pin_segments; DROP TABLE invocation_program_revision_pins; DROP TABLE invocation_program_deliveries; DROP TABLE invocation_program_outputs; DROP TABLE invocation_program_executions; DROP TABLE invocation_program_definitions; DROP TABLE invocation_program_progress; DROP TABLE invocation_program_nodes; DROP TABLE invocation_program_sources; DROP TABLE invocation_programs; DROP INDEX idx_evidence_records_source_delivery; PRAGMA user_version=5;`)
	testutil.FailErr(t, "derive actual known schema5 intermediate", err)
}

func suspendSourceFixtureViews(t *testing.T, tx *sql.Tx) []string {
	t.Helper()
	rows, err := tx.QueryContext(t.Context(), `SELECT name,sql FROM sqlite_master WHERE type='view' AND sql IS NOT NULL ORDER BY rowid`)
	testutil.FailErr(t, "read fixture views", err)
	type view struct{ name, definition string }
	var views []view
	for rows.Next() {
		var item view
		testutil.FailErr(t, "read fixture view", rows.Scan(&item.name, &item.definition))
		views = append(views, item)
	}
	testutil.FailErr(t, "finish fixture views", rows.Err())
	testutil.FailErr(t, "close fixture views", rows.Close())
	var definitions []string
	for _, item := range views {
		_, err := tx.ExecContext(t.Context(), `DROP VIEW "`+strings.ReplaceAll(item.name, `"`, `""`)+`"`)
		testutil.FailErr(t, "drop fixture view", err)
		definitions = append(definitions, item.definition)
	}
	return definitions
}

func suspendSourceFixtureTriggers(t *testing.T, tx *sql.Tx) []string {
	t.Helper()
	rows, err := tx.QueryContext(t.Context(), `SELECT name,sql FROM sqlite_master WHERE type='trigger' AND sql IS NOT NULL ORDER BY name`)
	testutil.FailErr(t, "read fixture triggers", err)
	type trigger struct{ name, definition string }
	var triggers []trigger
	for rows.Next() {
		var item trigger
		testutil.FailErr(t, "read fixture trigger", rows.Scan(&item.name, &item.definition))
		triggers = append(triggers, item)
	}
	testutil.FailErr(t, "finish fixture triggers", rows.Err())
	testutil.FailErr(t, "close fixture triggers", rows.Close())
	var definitions []string
	for _, item := range triggers {
		if !strings.Contains(item.definition, "source_files") && !strings.Contains(item.definition, "source_versions") &&
			!strings.Contains(item.definition, "source_head_entries") && !strings.Contains(item.definition, "source_effects") {
			continue
		}
		_, err := tx.ExecContext(t.Context(), `DROP TRIGGER "`+strings.ReplaceAll(item.name, `"`, `""`)+`"`)
		testutil.FailErr(t, "drop fixture source trigger", err)
		definitions = append(definitions, item.definition)
	}
	return definitions
}

func downgradeLinkTableFixture(t *testing.T, tx *sql.Tx, table string) {
	t.Helper()
	var ddl string
	testutil.FailErr(t, "read source table", tx.QueryRowContext(t.Context(), `SELECT sql FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&ddl))
	tail := ddl[strings.IndexByte(ddl, '('):]
	switch table {
	case "source_files", "source_effects":
		tail = strings.Replace(tail, "entry_kind IN ('file', 'directory', 'link')", "entry_kind IN ('file', 'directory')", 1)
	case "source_versions", "source_head_entries":
		tail = strings.Replace(tail, "state IN ('content', 'directory', 'link', 'absent', 'unresolved')", "state IN ('content', 'directory', 'absent', 'unresolved')", 1)
		if table == "source_versions" {
			tail = strings.Replace(tail, "state IN ('content', 'link') AND", "state = 'content' AND", 1)
		}
	}
	rows, err := tx.QueryContext(t.Context(), `SELECT sql FROM sqlite_master WHERE tbl_name=? AND type='index' AND sql IS NOT NULL ORDER BY name`, table)
	testutil.FailErr(t, "read source indexes", err)
	var definitions []string
	for rows.Next() {
		var statement string
		testutil.FailErr(t, "read source index", rows.Scan(&statement))
		definitions = append(definitions, statement)
	}
	testutil.FailErr(t, "finish source index list", rows.Err())
	testutil.FailErr(t, "close source index list", rows.Close())
	temporary := table + "_revision5"
	for _, statement := range []string{
		"CREATE TABLE " + temporary + " " + tail,
		"INSERT INTO " + temporary + " SELECT * FROM " + table,
		"DROP TABLE " + table,
		"ALTER TABLE " + temporary + " RENAME TO " + table,
	} {
		_, err := tx.ExecContext(t.Context(), statement)
		testutil.FailErr(t, "derive revision5 source table", err)
	}
	for _, statement := range definitions {
		_, err := tx.ExecContext(t.Context(), statement)
		testutil.FailErr(t, "restore revision5 source index", err)
	}
}

func TestProgramReceiptsUpgradeKnownIntermediateAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	source, err := Open(path)
	testutil.FailErr(t, "open isolated source", err)
	DeriveRevision5Fixture(t, source)
	_, err = source.ExecContext(t.Context(), `INSERT INTO projects(id,last_opened_at,created_at) VALUES('fixture-project','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'); INSERT INTO sessions(id,project_id,owner_person_id,posture,created_at,activity_at,updated_at) VALUES('fixture-session','fixture-project',(SELECT id FROM people WHERE role='owner'),'build','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z');`)
	testutil.FailErr(t, "seed retained actual session", err)
	_, err = source.ExecContext(t.Context(), `INSERT INTO source_files(id,project_id,entry_kind,created_ts) VALUES('fixture-file','fixture-project','file','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "seed retained source identity", err)
	_, err = source.ExecContext(t.Context(), `INSERT INTO invocation_receipts(id,project_id,session_id,tool_call_id,tool_name,owner,lifecycle,reversibility,evidence_policy,recovery_policy,evidence_kind,contract_digest,args_digest,status,started_at) VALUES('fixture-parent','fixture-project','fixture-session','actual-model-call','command','processes','effect_attempt','irreversible','attempt','abandon','pending','contract','args','running','2026-01-01T00:00:00Z')`)
	testutil.FailErr(t, "seed real parent receipt", err)
	target, err := CurrentBaseline(t.Context())
	testutil.FailErr(t, "inspect target", err)
	actual, err := inspectSchema(t.Context(), source)
	testutil.FailErr(t, "inspect known intermediate", err)
	if actual != migrations.ProgramReceipts(target).From {
		t.Fatalf("wrong intermediate: %+v", actual)
	}
	testutil.FailErr(t, "close isolated source", source.Close())
	testutil.FailErr(t, "upgrade actual registered route", UpgradeStaged(t.Context(), path))
	testutil.FailErr(t, "restart without repeating migration", UpgradeStaged(t.Context(), path))
	upgraded, err := sql.Open("sqlite", path)
	testutil.FailErr(t, "inspect upgraded database", err)
	defer upgraded.Close()
	actual, err = inspectSchema(t.Context(), upgraded)
	testutil.FailErr(t, "inspect upgraded shape", err)
	if actual != target {
		t.Fatalf("wrong upgraded target: %+v", actual)
	}
	var modelCall string
	testutil.FailErr(t, "preserve actual parent identity", upgraded.QueryRow(`SELECT tool_call_id FROM invocation_receipts WHERE id='fixture-parent'`).Scan(&modelCall))
	if modelCall != "actual-model-call" {
		t.Fatal("parent model call changed")
	}
	var entryKind string
	testutil.FailErr(t, "preserve source identity", upgraded.QueryRow(`SELECT entry_kind FROM source_files WHERE id='fixture-file'`).Scan(&entryKind))
	if entryKind != "file" {
		t.Fatalf("source entry kind = %q", entryKind)
	}
	var count int
	testutil.FailErr(t, "inspect migration ledger", upgraded.QueryRow(`SELECT count(*) FROM schema_migrations WHERE id='portable-program-receipts'`).Scan(&count))
	if count != 1 {
		t.Fatalf("migration applied %d times", count)
	}
	testutil.FailErr(t, "no fabricated children", upgraded.QueryRow(`SELECT count(*) FROM invocation_program_executions`).Scan(&count))
	if count != 0 {
		t.Fatal("migration fabricated host executions")
	}
}
