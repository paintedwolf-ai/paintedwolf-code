package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Loop wake policy keys on HumanApprovalAwaiting, never CurrentPhase == "approve".
func TestLoopWakeDoesNotDenyByPhaseName(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	dir := filepath.Join(root, "lycaon", "internal", "coordinator", "loopwake")
	fset := token.NewFileSet()
	var findings []string

	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(f, func(n ast.Node) bool {
			bin, ok := n.(*ast.BinaryExpr)
			if !ok || bin.Op != token.EQL && bin.Op != token.NEQ {
				return true
			}
			if !isCurrentPhaseSelector(bin.X) && !isCurrentPhaseSelector(bin.Y) {
				return true
			}
			lit := stringLit(bin.X)
			if lit == "" {
				lit = stringLit(bin.Y)
			}
			if lit == "approve" {
				pos := fset.Position(n.Pos())
				findings = append(findings, rel+":"+strconv.Itoa(pos.Line)+": CurrentPhase compared to phase name")
			}
			return true
		})
		return nil
	})
	contractcheck.FailErr(t, "walk loopwake", err)
	if len(findings) > 0 {
		t.Fatalf("Anti-drift: loop wake must not key on phase name:\n  %s", strings.Join(findings, "\n  "))
	}
}

func isCurrentPhaseSelector(n ast.Expr) bool {
	sel, ok := n.(*ast.SelectorExpr)
	return ok && sel.Sel != nil && sel.Sel.Name == "CurrentPhase"
}

func stringLit(n ast.Expr) string {
	lit, ok := n.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	s, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return s
}
