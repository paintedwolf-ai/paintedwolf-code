package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestSourceRecordingPortDoesNotRecoverPeerCapabilities(t *testing.T) {
	t.Parallel()
	fset, files := contractcheck.ParseNonTestGoTree(t, filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal"))
	for _, file := range files {
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			for _, assertion := range sourceRecorderAssertions(function.Body) {
				t.Errorf("%s: source recording dependencies must receive history, command, and checkpoint ports explicitly", fset.Position(assertion.Pos()))
			}
		}
	}
}

func sourceRecorderAssertions(body *ast.BlockStmt) []*ast.TypeAssertExpr {
	aliases := map[string]bool{}
	var isRecorder func(ast.Expr) bool
	isRecorder = func(expression ast.Expr) bool {
		switch expression := expression.(type) {
		case *ast.Ident:
			return expression.Name == "sourceLedger" || aliases[expression.Name]
		case *ast.SelectorExpr:
			return expression.Sel.Name == "SourceLedger" || expression.Sel.Name == "sourceLedger"
		case *ast.ParenExpr:
			return isRecorder(expression.X)
		}
		return false
	}
	for changed := true; changed; {
		changed = false
		ast.Inspect(body, func(node ast.Node) bool {
			assignment, ok := node.(*ast.AssignStmt)
			if !ok || len(assignment.Lhs) != len(assignment.Rhs) {
				return true
			}
			for index, expression := range assignment.Rhs {
				name, ok := assignment.Lhs[index].(*ast.Ident)
				if ok && isRecorder(expression) && !aliases[name.Name] {
					aliases[name.Name] = true
					changed = true
				}
			}
			return true
		})
	}
	var assertions []*ast.TypeAssertExpr
	ast.Inspect(body, func(node ast.Node) bool {
		assertion, ok := node.(*ast.TypeAssertExpr)
		if ok && isRecorder(assertion.X) {
			assertions = append(assertions, assertion)
		}
		return true
	})
	return assertions
}

func TestSourceRecorderAssertionScan(t *testing.T) {
	for _, fixture := range []struct {
		name, body string
		count      int
	}{
		{"direct", "_ = tc.Source.SourceLedger.(History)", 1},
		{"alias", "ledger := tc.Source.SourceLedger; reader := ledger; _ = reader.(History)", 1},
		{"type switch", "switch tc.Source.SourceLedger.(type) { case History: }", 1},
		{"explicit port", "_ = tc.Source.History.Files.(History)", 0},
		{"unrelated recorder", "_ = events.Recorder.(Events)", 0},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			file, err := parser.ParseFile(token.NewFileSet(), "fixture.go", "package fixture; func check() {"+fixture.body+"}", 0)
			if err != nil {
				contractcheck.FailErr(t, "parse source port fixture", err)
			}
			body := file.Decls[0].(*ast.FuncDecl).Body
			if got := len(sourceRecorderAssertions(body)); got != fixture.count {
				t.Fatalf("assertions = %d, want %d", got, fixture.count)
			}
		})
	}
}
