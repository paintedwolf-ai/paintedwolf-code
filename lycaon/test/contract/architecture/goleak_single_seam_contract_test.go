package contract

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configlayout"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// goleakSeamPackage centralizes leak teardown and ignores.
const goleakSeamPackage = "internal/testutil/"

// Every package uses testutil.VerifyNoLeaks for shared teardown and ignores.
func TestGoleakIsInvokedThroughOneSeam(t *testing.T) {
	t.Parallel()
	corp, err := contractcheck.LoadGoASTCorpus(configlayout.FindModuleRoot())
	contractcheck.FailErr(t, "load Go AST corpus", err)

	var violations []string
	for _, f := range corp.Files() {
		if strings.HasPrefix(f.Rel, goleakSeamPackage) {
			continue
		}
		ast.Inspect(f.AST, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "goleak" {
				return true
			}
			violations = append(violations, fmt.Sprintf(
				"%s: calls goleak.%s directly — go through testutil.VerifyNoLeaks so the ignore list and teardown hook stay shared",
				corp.Fset.Position(call.Pos()), sel.Sel.Name))
			return true
		})
	}
	if len(corp.Files()) == 0 {
		t.Fatal("empty Go AST corpus; the scan is not testing anything")
	}
	contractcheck.FailViolations(t, "goleak invoked outside its single seam", violations)
}
