package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/lycaon/lycaon/internal/db"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestFreshWipeProductionGate(t *testing.T) {
	t.Setenv("LYCAON_DB_FRESH", "1")
	t.Setenv("LYCAON_CONFIG_DIR", "")
	t.Setenv("LYCAON_DEV", "")
	if db.FreshEnabled() {
		t.Fatal("LYCAON_DB_FRESH=1 must be ignored on the production channel")
	}
	if !db.FreshRequested() {
		t.Fatal("FreshRequested must still report the flag so serve can log the refusal")
	}
	t.Setenv("LYCAON_DEV", "1")
	if !db.FreshEnabled() {
		t.Fatal("development channel must honor LYCAON_DB_FRESH=1")
	}

	root := contractcheck.RepoRoot(t)
	owner := freshGateSource(t, root, "persistence/runtime.go")
	open := freshGateFunction(t, owner, "Open")
	resets := freshGateCalls(open, "localdata.ResetStoreCoupled")
	if len(resets) != 1 || len(resets[0].Args) != 2 || freshGateSelector(resets[0].Args[0]) != "ctx" || freshGateSelector(resets[0].Args[1]) != "dbPath" {
		t.Fatal("storage must reset exactly its selected store once with the startup context")
	}
	guarded := false
	ast.Inspect(open, func(n ast.Node) bool {
		clause, ok := n.(*ast.CaseClause)
		if !ok || len(clause.List) != 1 {
			return true
		}
		condition, ok := clause.List[0].(*ast.CallExpr)
		if !ok || freshGateSelector(condition.Fun) != "db.FreshEnabled" || len(condition.Args) != 0 {
			return true
		}
		for _, call := range freshGateCalls(clause, "localdata.ResetStoreCoupled") {
			if call == resets[0] {
				guarded = true
			}
		}
		return true
	})
	if !guarded {
		t.Fatal("store reset must be inside the development-only FreshEnabled branch")
	}
	opens := freshGateCalls(open, "b.openUpgradeableStore")
	if len(opens) != 1 || resets[0].Pos() >= opens[0].Pos() {
		t.Fatal("development reset must precede opening the selected store")
	}
	build := freshGateFunction(t, freshGateSource(t, root, "build.go"), "Build")
	storageOpens := freshGateCalls(build, "b.storage.Open")
	if len(storageOpens) != 1 || len(storageOpens[0].Args) != 5 || freshGateSelector(storageOpens[0].Args[0]) != "ctx" || freshGateSelector(storageOpens[0].Args[1]) != "path" {
		t.Fatal("bootstrap must open the selected path with its startup context through storage")
	}
}

func freshGateSource(t *testing.T, root, owner string) *ast.File {
	t.Helper()
	source := contractcheck.ReadRepoFile(t, root, "lycaon/internal/app/"+owner)
	file, err := parser.ParseFile(token.NewFileSet(), owner, source, parser.SkipObjectResolution)
	contractcheck.FailErr(t, "parse fresh gate owner", err)
	return file
}

func freshGateFunction(t *testing.T, file *ast.File, name string) *ast.FuncDecl {
	t.Helper()
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == name {
			return fn
		}
	}
	t.Fatalf("fresh gate owner missing %s", name)
	return nil
}

func freshGateCalls(node ast.Node, callee string) []*ast.CallExpr {
	var calls []*ast.CallExpr
	ast.Inspect(node, func(n ast.Node) bool {
		if call, ok := n.(*ast.CallExpr); ok && freshGateSelector(call.Fun) == callee {
			calls = append(calls, call)
		}
		return true
	})
	return calls
}

func freshGateSelector(expr ast.Expr) string {
	switch node := expr.(type) {
	case *ast.Ident:
		return node.Name
	case *ast.SelectorExpr:
		return freshGateSelector(node.X) + "." + node.Sel.Name
	}
	return ""
}
