package migrations_test

import (
	"database/sql"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
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
	_, err = database.Exec(`CREATE TABLE checkpoints(id TEXT PRIMARY KEY,kind TEXT,payload_json TEXT); CREATE TABLE approval_operations(checkpoint_id TEXT PRIMARY KEY); CREATE TABLE chat_grants(id TEXT PRIMARY KEY)`)
	testutil.FailErr(t, "create retained tables", err)
	_, err = database.Exec(`INSERT INTO checkpoints VALUES('fixture','tool_approval',?)`, string(payload))
	testutil.FailErr(t, "persist unrecognized retained authority", err)
	tx, err := database.BeginTx(t.Context(), nil)
	testutil.FailErr(t, "begin refused migration", err)
	if err := migrations.EffectPermitIdentity(migrations.Baseline{Revision: 5}).Apply(t.Context(), tx); err == nil {
		t.Fatal("migration accepted unrecognized retained authority")
	}
	testutil.FailErr(t, "roll back refused migration", tx.Rollback())
	var raw string
	var added int
	testutil.FailErr(t, "read unchanged checkpoint", database.QueryRow(`SELECT payload_json FROM checkpoints WHERE id='fixture'`).Scan(&raw))
	testutil.FailErr(t, "read rolled back schema", database.QueryRow(`SELECT count(*) FROM pragma_table_info('checkpoints') WHERE name LIKE 'authority_%'`).Scan(&added))
	if raw != string(payload) || added != 0 {
		t.Fatal("refused migration changed retained authority or its schema")
	}
}
