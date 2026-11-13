package migrations

import (
	"database/sql"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

const (
	singleEditResult = `{"tool_call_id":"c1","file_edit":{"path":"a.go","before":"old","after":"new"},"outcome":"completed"}`
	singleEditSpans  = `{"spans":[{"field":"tool_result.file_edit.after","start":2,"length":5},{"field":"tool_result.file_edit.before","start":0,"length":3},{"field":"content","start":1,"length":4}]}`
)

func fileEditMigrationFixture(t *testing.T, rows ...string) *sql.DB {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	testutil.FailErr(t, "open file edit fixture", err)
	t.Cleanup(func() { _ = database.Close() })
	database.SetMaxOpenConns(1)
	_, err = database.Exec(`CREATE TABLE messages(id TEXT PRIMARY KEY, role TEXT NOT NULL, content TEXT,
 tool_result_json TEXT CHECK (tool_result_json = '' OR json_valid(tool_result_json)),
 host_secret_redaction_json TEXT CHECK (host_secret_redaction_json = '' OR json_valid(host_secret_redaction_json)))`)
	testutil.FailErr(t, "create released messages", err)
	for _, row := range rows {
		_, err = database.Exec(row)
		testutil.FailErr(t, "seed released message", err)
	}
	return database
}

func applyFileEditMigration(t *testing.T, database *sql.DB) (*sql.Tx, error) {
	t.Helper()
	tx, err := database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin file edit migration", err)
	t.Cleanup(func() { _ = tx.Rollback() })
	part := ToolResultFileEdits()
	testutil.FailErr(t, "move file edits", part.Apply(t.Context(), tx))
	return tx, part.Validate(t.Context(), tx)
}

func TestToolResultFileEditsMoveToTheListWithTheirSpans(t *testing.T) {
	database := fileEditMigrationFixture(t,
		`INSERT INTO messages VALUES('m1','tool','ok','`+singleEditResult+`','`+singleEditSpans+`')`,
		`INSERT INTO messages VALUES('m2','tool','ok','{"tool_call_id":"c2","file_edit":null}',NULL)`,
		`INSERT INTO messages VALUES('m3','tool','ok','{"tool_call_id":"c3"}','')`,
		`INSERT INTO messages VALUES('m4','assistant','text','',NULL)`,
	)
	written, err := ToolResultFileEdits().Writes(t.Context(), database, 1<<30)
	testutil.FailErr(t, "estimate file edit writes", err)
	if written == 0 || written >= 1<<20 {
		t.Fatalf("estimated %d bytes for two small rows", written)
	}
	tx, err := applyFileEditMigration(t, database)
	testutil.FailErr(t, "validate moved file edits", err)

	var edits, moved, callID, outcome, spans string
	testutil.FailErr(t, "read moved edit", tx.QueryRow(`SELECT json_type(tool_result_json,'$.file_edits'), json_extract(tool_result_json,'$.file_edits[0]'),
 json_extract(tool_result_json,'$.tool_call_id'), json_extract(tool_result_json,'$.outcome'), host_secret_redaction_json FROM messages WHERE id='m1'`).Scan(&edits, &moved, &callID, &outcome, &spans))
	if edits != "array" || moved != `{"path":"a.go","before":"old","after":"new"}` || callID != "c1" || outcome != "completed" {
		t.Fatalf("moved edit = %s %s, kept %s %s", edits, moved, callID, outcome)
	}
	// The dotted index is the field name the redactor writes for the first edit.
	want := `{"spans":[{"field":"tool_result.file_edits.0.after","start":2,"length":5},{"field":"tool_result.file_edits.0.before","start":0,"length":3},{"field":"content","start":1,"length":4}]}`
	if spans != want {
		t.Fatalf("spans = %s\nwant    %s", spans, want)
	}
	var remaining, lists int
	testutil.FailErr(t, "count shapes", tx.QueryRow(`SELECT COALESCE(SUM(json_type(NULLIF(tool_result_json,''),'$.file_edit') IS NOT NULL),0),
 COALESCE(SUM(json_type(NULLIF(tool_result_json,''),'$.file_edits') IS NOT NULL),0) FROM messages`).Scan(&remaining, &lists))
	if remaining != 0 || lists != 1 {
		t.Fatalf("%d single edits remain and %d rows hold a list, want 0 and 1", remaining, lists)
	}
}

func TestToolResultFileEditsCostNothingWithoutEdits(t *testing.T) {
	database := fileEditMigrationFixture(t, `INSERT INTO messages VALUES('m1','tool','ok','{"tool_call_id":"c1"}','')`)
	written, err := ToolResultFileEdits().Writes(t.Context(), database, 1<<30)
	testutil.FailErr(t, "estimate file edit writes", err)
	if written != 0 {
		t.Fatalf("estimated %d bytes for a store with no file edits", written)
	}
}

func TestToolResultFileEditsRefuseAnUnknownShape(t *testing.T) {
	database := fileEditMigrationFixture(t,
		`INSERT INTO messages VALUES('m1','tool','ok','`+singleEditResult+`',NULL)`,
		`INSERT INTO messages VALUES('m2','tool','ok','{"file_edit":"a.go"}',NULL)`,
	)
	if _, err := applyFileEditMigration(t, database); err == nil {
		t.Fatal("a file edit that is not an object passed validation")
	}
}

func TestToolResultFileEditsCoverEveryBatch(t *testing.T) {
	database := fileEditMigrationFixture(t)
	insert, err := database.Prepare(`INSERT INTO messages VALUES(?,'tool','ok',?,NULL)`)
	testutil.FailErr(t, "prepare seed", err)
	const rows = 600
	for i := range rows {
		_, err := insert.Exec(sql.Named("id", i).Value, singleEditResult)
		testutil.FailErr(t, "seed row", err)
	}
	testutil.FailErr(t, "close seed", insert.Close())
	tx, err := applyFileEditMigration(t, database)
	testutil.FailErr(t, "validate moved file edits", err)
	var lists int
	testutil.FailErr(t, "count moved", tx.QueryRow(`SELECT COUNT(*) FROM messages WHERE json_type(tool_result_json,'$.file_edits[0]')='object'`).Scan(&lists))
	if lists != rows {
		t.Fatalf("moved %d of %d rows", lists, rows)
	}
}
