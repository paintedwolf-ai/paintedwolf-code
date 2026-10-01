package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Database mutation results must be returned, logged, or counted.
var observedMutationMethods = map[string]bool{
	"Exec":        true,
	"ExecContext": true,
}

func TestNoUnobservedDatabaseMutations(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, dir := range []string{"internal", "cmd", "pkg"} {
		assertNoUnobservedMutations(t, filepath.Join(root, "lycaon", dir))
	}
}

func assertNoUnobservedMutations(t *testing.T, dir string) {
	t.Helper()
	fset := token.NewFileSet()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Tests may discard a fixture write; production code may not.
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return parseErr
		}
		ast.Inspect(file, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || !allBlank(assign.Lhs) || len(assign.Rhs) != 1 {
				return true
			}
			call, ok := assign.Rhs[0].(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !observedMutationMethods[sel.Sel.Name] {
				return true
			}
			t.Errorf("%s: %s result is discarded into blank identifiers — a mutation "+
				"nobody observes cannot report that it stopped working. Return the "+
				"error, or hand it to a seam that counts and logs it.",
				fset.Position(assign.Pos()), sel.Sel.Name)
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "walk "+dir, err)
}

// allBlank reports whether every assignment target is the blank identifier.
func allBlank(lhs []ast.Expr) bool {
	for _, e := range lhs {
		ident, ok := e.(*ast.Ident)
		if !ok || ident.Name != "_" {
			return false
		}
	}
	return len(lhs) > 0
}
