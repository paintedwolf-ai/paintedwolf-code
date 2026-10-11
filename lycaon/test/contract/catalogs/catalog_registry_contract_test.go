package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
	"github.com/lycaon/lycaon/test/contract/internal/toolfixture"
)

func TestCatalogAllowlistRegistrySync(t *testing.T) {
	for _, callee := range []string{"delegation.RegisterDelegationTools", "parse.RegisterParseTools", "workflowstatetools.RegisterStateTools", "blueprint.RegisterPlanTools"} {
		requireCatalogCall(t, "build_tools.go", "registerCoordinatorTools", callee, "b.execution.Host.Registry")
	}
	requireCatalogCall(t, "build_board.go", "wireGroundingAndFindings", "b.boards.WireGroundingAndFindings", "b.startup.ctx")
	requireCatalogCall(t, "boards/grounding.go", "WireGroundingAndFindings", "r.wireDecisionAndCallTools", "")
	requireCatalogCall(t, "boards/grounding.go", "wireDecisionAndCallTools", "call.RegisterHandoffTools", "r.deps.Execution.Host.Registry")

	reg := toolfixture.RegisterCatalogToolsForContract(t)
	if err := tools.ValidateCatalogAllowlistSync(toolfixture.BootRegisteredToolSet(t, reg)); err != nil {
		contractcheck.FailErr(t, "tools.ValidateCatalogAllowlistSync failed", err)
	}
}

func requireCatalogCall(t *testing.T, owner, function, callee, firstArg string) {
	t.Helper()
	source := contractcheck.ReadRepoFile(t, contractcheck.RepoRoot(t), "lycaon/internal/app/"+owner)
	file, err := parser.ParseFile(token.NewFileSet(), owner, source, parser.SkipObjectResolution)
	contractcheck.FailErr(t, "parse catalog composition owner", err)
	found := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != function {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || catalogSelector(call.Fun) != callee {
				return true
			}
			if firstArg != "" && (len(call.Args) == 0 || catalogSelector(call.Args[0]) != firstArg) {
				t.Fatalf("%s.%s must pass its canonical registry or context to %s", owner, function, callee)
			}
			found = true
			return true
		})
	}
	if !found {
		t.Fatalf("%s.%s missing composition call %s", owner, function, callee)
	}
}

func catalogSelector(expr ast.Expr) string {
	switch node := expr.(type) {
	case *ast.Ident:
		return node.Name
	case *ast.SelectorExpr:
		return catalogSelector(node.X) + "." + node.Sel.Name
	}
	return ""
}
