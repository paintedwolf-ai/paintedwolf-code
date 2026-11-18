package contract

import (
	"go/ast"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestProviderHTTPUsesEgressGuard(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "webresearch")
	for caller, callees := range map[string][]string{
		"doProviderHTTP":                 {"egressgate.AwaitHost", "validateProviderEndpoint", "providerHTTPThroughDoor"},
		"providerHTTPThroughDoor":        {"providerHop", "ipProviderEgress", "exchange"},
		"providerHop":                    {"validateProviderEndpoint", "egressgate.AwaitHost"},
		"validateProviderEndpoint":       {"ipProviderEgress"},
		"ValidateProviderConfigEndpoint": {"resolveProviderEgressIPs"},
	} {
		for _, callee := range callees {
			if !funcCallsNamed(findFuncDeclInDir(t, dir, caller), callee) {
				t.Errorf("%s must use %s", caller, callee)
			}
		}
	}
	boundedDefault := false
	ast.Inspect(findFuncDeclInDir(t, dir, "providerHTTPThroughDoor").Body, func(n ast.Node) bool {
		assignment, ok := n.(*ast.AssignStmt)
		if !ok || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
			return true
		}
		name, ok := assignment.Lhs[0].(*ast.Ident)
		if !ok || name.Name != "exchange" {
			return true
		}
		value, ok := assignment.Rhs[0].(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := value.X.(*ast.Ident)
		boundedDefault = ok && pkg.Name == "outboundhttp" && value.Sel.Name == "Do"
		return true
	})
	if !boundedDefault {
		t.Error("provider exchange must default to outboundhttp.Do")
	}
	outbound := filepath.Join(contractcheck.RepoRoot(t), "lycaon", "internal", "outboundhttp")
	for caller, callees := range map[string][]string{
		"Do":              {"in.BeforeHop", "in.transportForHop"},
		"transportForHop": {"egress.ResolveIPsWithPolicy", "in.addressAllowed", "egress.PinnedTransport"},
	} {
		for _, callee := range callees {
			if !funcCallsNamed(findFuncDeclInDir(t, outbound, caller), callee) {
				t.Errorf("outboundhttp.%s must use %s", caller, callee)
			}
		}
	}
	fset, files := contractcheck.ParseNonTestGoTree(t, dir)
	for _, file := range files {
		ast.Inspect(file, func(n ast.Node) bool {
			selector, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if ok && pkg.Name == "http" && (selector.Sel.Name == "DefaultClient" || selector.Sel.Name == "ProxyFromEnvironment") {
				t.Errorf("%s: web research bypasses guarded transport with http.%s", fset.Position(n.Pos()), selector.Sel.Name)
			}
			return true
		})
	}
}
