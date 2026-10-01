package contract

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestRegisterCallsUseAllowedNames(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	codeRoot := filepath.Join(root, "lycaon")
	var violations []string
	corpus, err := contractcheck.LoadGoASTCorpus(codeRoot)
	contractcheck.FailErr(t, "load lycaon Go corpus", err)
	for _, source := range corpus.Files() {
		if source.IsTest {
			continue
		}
		ast.Inspect(source.AST, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := registerCallName(call)
			if name == "" {
				return true
			}
			if conditions.IsForbidden(name) {
				pos := corpus.Fset.Position(call.Pos())
				violations = append(violations, pos.Filename+":"+strconv.Itoa(pos.Line)+": forbidden Register name "+name)
			}
			return true
		})
	}
	contractcheck.FailViolations(t, "Register() called with forbidden condition name", violations)
}

func registerCallName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		if fn.Name != "Register" {
			return ""
		}
	case *ast.SelectorExpr:
		if fn.Sel.Name != "Register" {
			return ""
		}
	default:
		return ""
	}
	if len(call.Args) == 0 {
		return ""
	}
	lit, ok := call.Args[0].(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return ""
	}
	return strings.Trim(lit.Value, `"`)
}
