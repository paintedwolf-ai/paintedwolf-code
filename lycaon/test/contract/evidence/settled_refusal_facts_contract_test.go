package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestCatalogToolProducersDoNotStateUncodedErrorOrRejected(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	toolsDir := filepath.Join(root, "lycaon", "internal", "tools")
	err := filepath.WalkDir(toolsDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Errorf("parse %s: %v", path, parseErr)
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name != "ToolResultOutcomeError" && sel.Sel.Name != "ToolResultOutcomeRejected" {
				return true
			}
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s:%d: catalog tools stated %s", rel, fset.Position(sel.Pos()).Line, sel.Sel.Name)
			return true
		})
		return nil
	})
	testutil.FailErr(t, "walk internal/tools", err)
}
