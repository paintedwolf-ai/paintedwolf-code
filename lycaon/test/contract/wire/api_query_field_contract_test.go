package contract

import (
	"go/ast"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestAPIQueryDiagnosticsNameOneField(t *testing.T) {
	corpus, err := contractcheck.LoadGoASTCorpus(filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "api"))
	contractcheck.FailErr(t, "load API source", err)
	fieldName := regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	for _, file := range corpus.Production() {
		ast.Inspect(file.AST, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) < 2 {
				return true
			}
			method, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || method.Sel.Name != "InvalidQueryParam" {
				return true
			}
			literal, ok := call.Args[1].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			field, err := strconv.Unquote(literal.Value)
			if err != nil || !fieldName.MatchString(field) {
				t.Errorf("%s: query diagnostic must identify one snake_case field, got %s", corpus.Fset.Position(literal.Pos()), literal.Value)
			}
			return true
		})
	}
}
