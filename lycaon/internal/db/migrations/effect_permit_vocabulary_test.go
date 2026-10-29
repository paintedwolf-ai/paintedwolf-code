package migrations_test

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/db/migrations"
	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/testutil"
)

func requireAuthorityVocabularyCoverage(t *testing.T, covered map[hitl.AuthorityDeltaKind]bool) {
	t.Helper()
	root := filepath.Join(testutil.CheckoutRoot(t), "lycaon", "internal", "hitl")
	packages, err := parser.ParseDir(token.NewFileSet(), root, func(info os.FileInfo) bool {
		return !strings.HasSuffix(info.Name(), "_test.go")
	}, parser.SkipObjectResolution)
	testutil.FailErr(t, "read authority owner declarations", err)
	declared := map[hitl.AuthorityDeltaKind]bool{}
	for _, file := range packages["hitl"].Files {
		for _, declaration := range file.Decls {
			group, ok := declaration.(*ast.GenDecl)
			if !ok || group.Tok != token.CONST {
				continue
			}
			var previous *ast.ValueSpec
			for _, specification := range group.Specs {
				value := specification.(*ast.ValueSpec)
				effective := value
				if len(value.Values) == 0 {
					effective = previous
				}
				previous = effective
				if effective == nil {
					continue
				}
				kind, ok := effective.Type.(*ast.Ident)
				if !ok || kind.Name != "AuthorityDeltaKind" {
					continue
				}
				for index, name := range value.Names {
					literal, ok := effective.Values[index].(*ast.BasicLit)
					if !ok || literal.Kind != token.STRING {
						t.Fatalf("authority declaration %s needs vocabulary review", name.Name)
					}
					decoded, err := strconv.Unquote(literal.Value)
					testutil.FailErr(t, "read declared authority kind", err)
					declared[hitl.AuthorityDeltaKind(decoded)] = true
				}
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("authority owner vocabulary was not found")
	}
	for kind := range declared {
		if !covered[kind] {
			t.Errorf("declared authority %q has no validated migration fixture", kind)
		}
	}
	for kind := range covered {
		if !declared[kind] {
			t.Errorf("migration fixture uses undeclared authority %q", kind)
		}
	}
}

func assertUnrecognizedAuthorityRefused(t *testing.T, plan *hitl.ApprovalPlan) {
	t.Helper()
	original, err := json.Marshal(map[string]any{"approval_plan": plan})
	testutil.FailErr(t, "encode valid approval fixture", err)
	for optionIndex, option := range plan.Options {
		for deltaIndex, delta := range option.Authority {
			mutations := map[string]func(map[string]any){
				"unknown kind":   func(d map[string]any) { d["kind"] = "future_authority" },
				"unknown field":  func(d map[string]any) { d["future_grant"] = true },
				"child identity": func(d map[string]any) { d["execution_id"] = "invented-child" },
			}
			if len(delta.Sockets) > 0 {
				mutations["missing socket boundary"] = func(d map[string]any) { delete(d, "sockets") }
			}
			if delta.DirectIPLease != nil {
				mutations["missing IP boundary"] = func(d map[string]any) { delete(d, "direct_ip_lease") }
			}
			for name, mutate := range mutations {
				t.Run(string(delta.Kind)+"/"+name, func(t *testing.T) {
					var payload map[string]any
					testutil.FailErr(t, "decode mutation fixture", json.Unmarshal(original, &payload))
					options := payload["approval_plan"].(map[string]any)["options"].([]any)
					deltas := options[optionIndex].(map[string]any)["authority"].([]any)
					mutate(deltas[deltaIndex].(map[string]any))
					raw, err := json.Marshal(payload)
					testutil.FailErr(t, "encode unrecognized authority", err)
					assertApprovalMigrationRollback(t, raw)
				})
			}
		}
	}
}

func assertApprovalMigrationRollback(t *testing.T, payload []byte) {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "fixture.db"))
	testutil.FailErr(t, "open refusal fixture", err)
	defer database.Close()
	_, err = database.Exec(`
CREATE TABLE checkpoints(id TEXT PRIMARY KEY,kind TEXT,payload_json TEXT);
CREATE TABLE approval_operations(checkpoint_id TEXT PRIMARY KEY,session_id TEXT,option_id TEXT,option_json TEXT,status TEXT,committed_at TEXT);
CREATE INDEX retained_operation_status ON approval_operations(status);
CREATE TABLE chat_grants(chat_session_id TEXT,id TEXT,checkpoint_id TEXT,delta_json TEXT,expires_at TEXT,PRIMARY KEY(chat_session_id,id)) WITHOUT ROWID;
CREATE INDEX retained_grant_id ON chat_grants(id);
INSERT INTO approval_operations VALUES
 ('fixture','session','option','{"retained":"prepared"}','prepared',NULL),
 ('unrelated','other-session','other-option','{"retained":"committed"}','committed','2026-10-01T00:00:00Z');
INSERT INTO chat_grants VALUES
 ('session','grant','fixture','{"retained":"chat"}',NULL),
 ('other-session','other-grant','unrelated','{"retained":"day"}','2026-10-02T00:00:00Z');`)
	testutil.FailErr(t, "create populated retained tables", err)
	_, err = database.Exec(`INSERT INTO checkpoints VALUES('fixture','tool_approval',?),('unrelated','content_apply','{"retained":true}')`, string(payload))
	testutil.FailErr(t, "persist unrecognized retained authority", err)
	before := approvalDatabaseSnapshot(t, database)
	tx, err := database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin refused migration", err)
	if err := migrations.EffectPermitIdentity(migrations.Baseline{Revision: 5}).Apply(t.Context(), tx); err == nil {
		t.Fatal("migration accepted unrecognized retained authority")
	}
	testutil.FailErr(t, "roll back refused migration", tx.Rollback())
	after := approvalDatabaseSnapshot(t, database)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("refused migration changed retained table data or complete SQLite schema")
	}
}

func approvalDatabaseSnapshot(t *testing.T, database *sql.DB) map[string][]string {
	t.Helper()
	state := map[string][]string{"schema": approvalQuerySnapshot(t, database, "SELECT * FROM sqlite_schema")}
	rows, err := database.Query(`SELECT name FROM sqlite_schema WHERE type='table'`)
	testutil.FailErr(t, "enumerate retained tables", err)
	var tables []string
	for rows.Next() {
		var table string
		testutil.FailErr(t, "read retained table name", rows.Scan(&table))
		tables = append(tables, table)
	}
	testutil.FailErr(t, "enumerate all retained tables", rows.Err())
	testutil.FailErr(t, "close retained table inventory", rows.Close())
	for _, table := range tables {
		quoted := `"` + strings.ReplaceAll(table, `"`, `""`) + `"`
		state["table:"+table] = approvalQuerySnapshot(t, database, "SELECT * FROM "+quoted)
	}
	return state
}

func approvalQuerySnapshot(t *testing.T, database *sql.DB, query string) []string {
	t.Helper()
	rows, err := database.Query(query)
	testutil.FailErr(t, "read retained state", err)
	defer rows.Close()
	columns, err := rows.Columns()
	testutil.FailErr(t, "read retained columns", err)
	var records []string
	for rows.Next() {
		values, destinations := make([]any, len(columns)), make([]any, len(columns))
		for index := range values {
			destinations[index] = &values[index]
		}
		testutil.FailErr(t, "read complete retained row", rows.Scan(destinations...))
		cells := make([]string, len(values))
		for index, value := range values {
			cells[index] = fmt.Sprintf("%T:%#v", value, value)
		}
		encoded, err := json.Marshal(cells)
		testutil.FailErr(t, "encode typed retained row", err)
		records = append(records, string(encoded))
	}
	testutil.FailErr(t, "read all retained rows", rows.Err())
	sort.Strings(records)
	header, err := json.Marshal(columns)
	testutil.FailErr(t, "encode retained columns", err)
	return append([]string{string(header)}, records...)
}
