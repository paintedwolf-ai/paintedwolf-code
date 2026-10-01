package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestProjectSearchSchemaLockFixture(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	lockPath := filepath.Join(root, "lycaon", "internal", "search", "schema.lock.json")
	data, err := os.ReadFile(lockPath)
	testutil.FailErr(t, "read schema.lock.json", err)
	text := string(data)
	for _, needle := range []string{
		`"project_id"`,
		`"not_null": true`,
		`"idx_evidence_index_project_kind_ts"`,
		`"messages_fts"`,
		`"evidence_fts"`,
		`"default_scope": "global"`,
		`"project_id_role"`,
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("schema.lock.json missing %q", needle)
		}
	}
}

func TestProjectSearchDSLGrammarIncludesOptionalProjectScope(t *testing.T) {
	t.Parallel()
	if !slices.Contains(search.DSLFieldAllowlist(), "project") {
		t.Fatal("DSLFieldAllowlist must include project for optional scope narrowing")
	}
	for _, field := range []string{"kind", "source", "trust", "verified", "tool", "path", "session"} {
		if !slices.Contains(search.DSLFieldAllowlist(), field) {
			t.Fatalf("DSLFieldAllowlist missing required field %q", field)
		}
	}
	if search.ProjectScopeCurrent != "current" {
		t.Fatalf("ProjectScopeCurrent = %q", search.ProjectScopeCurrent)
	}
	if search.DSLDefaultScope != "global" {
		t.Fatalf("DSLDefaultScope = %q", search.DSLDefaultScope)
	}
}

func TestProjectSearchExportFormatsLocked(t *testing.T) {
	t.Parallel()
	want := []string{"jsonl", "csv", "sarif"}
	if !reflect.DeepEqual(search.ExportFormats(), want) {
		t.Fatalf("ExportFormats = %v, want %v", search.ExportFormats(), want)
	}
}

func TestProjectSearchExecutorInterfaceDeclared(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "search", "federation.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	testutil.FailErr(t, "parse federation.go", err)
	var executor *ast.InterfaceType
	ast.Inspect(f, func(n ast.Node) bool {
		gen, ok := n.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			return true
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if ok && ts.Name.Name == "Executor" {
				executor, _ = ts.Type.(*ast.InterfaceType)
			}
		}
		return true
	})
	if executor == nil {
		t.Fatal("Executor interface not declared in federation.go")
	}
	if len(executor.Methods.List) < 2 {
		t.Fatal("Executor interface must declare Source and Run")
	}
}
