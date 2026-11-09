package migrations

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestTurnLoadVisibilityPreservesReceiptsAndConstraints(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	testutil.FailErr(t, "open receipt fixture", err)
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)
	_, err = database.Exec(`CREATE TABLE sessions(id TEXT PRIMARY KEY); CREATE TABLE messages(id TEXT PRIMARY KEY);
 CREATE TABLE turn_load_receipts (
 id INTEGER PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
 opening_message_id TEXT REFERENCES messages(id) ON DELETE CASCADE, tool_call_id TEXT NOT NULL,
 trigger TEXT NOT NULL CHECK(trigger IN ('turn','request','lookup','tool_event')), surface_id TEXT NOT NULL,
 engine TEXT NOT NULL, catalog_revision TEXT NOT NULL, state_json TEXT NOT NULL, decisions_json TEXT NOT NULL,
 standing_json TEXT NOT NULL CHECK(json_valid(standing_json)), elapsed_ms INTEGER NOT NULL CHECK(elapsed_ms >= 0),
 abstained INTEGER NOT NULL CHECK(abstained IN (0,1)), reason TEXT NOT NULL, created_at TEXT NOT NULL) STRICT;
 INSERT INTO sessions VALUES('s'); INSERT INTO messages VALUES('u');
 INSERT INTO turn_load_receipts VALUES(7,'s','u','','turn','implement','engine','rev','{}','{}','{"tools":[]}',12,0,'','timestamp');`)
	testutil.FailErr(t, "seed released receipt", err)
	tx, err := database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin receipt migration", err)
	defer func() { _ = tx.Rollback() }()
	testutil.FailErr(t, "migrate receipt vocabulary", TurnLoadVisibility().Apply(t.Context(), tx))
	var standing, engine string
	var elapsed int
	testutil.FailErr(t, "read retained receipt", tx.QueryRow(`SELECT standing_json,engine,elapsed_ms FROM turn_load_receipts WHERE id=7`).Scan(&standing, &engine, &elapsed))
	if standing != `{"tools":[]}` || engine != "engine" || elapsed != 12 {
		t.Fatalf("retained receipt = %s %s %d", standing, engine, elapsed)
	}
	_, err = tx.Exec(`INSERT INTO turn_load_receipts SELECT 8,session_id,opening_message_id,tool_call_id,'visibility',surface_id,'',catalog_revision,state_json,decisions_json,standing_json,0,1,'unavailable',created_at FROM turn_load_receipts WHERE id=7`)
	testutil.FailErr(t, "record visibility", err)
	if _, err := tx.Exec(`UPDATE turn_load_receipts SET trigger='unknown' WHERE id=8`); err == nil {
		t.Fatal("unknown trigger accepted")
	}
	var definition string
	testutil.FailErr(t, "read migrated schema", tx.QueryRow(`SELECT sql FROM sqlite_schema WHERE name='turn_load_receipts'`).Scan(&definition))
	if !strings.Contains(definition, "ON DELETE CASCADE") || !strings.Contains(definition, "STRICT") {
		t.Fatalf("lost receipt constraints: %s", definition)
	}
	var indexes int
	testutil.FailErr(t, "count receipt indexes", tx.QueryRow(`SELECT count(*) FROM sqlite_schema WHERE type='index' AND tbl_name='turn_load_receipts'`).Scan(&indexes))
	if indexes != 2 {
		t.Fatalf("receipt indexes = %d", indexes)
	}
	testutil.FailErr(t, "commit receipt migration", tx.Commit())
}
