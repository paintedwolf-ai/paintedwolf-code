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
	for caller, callee := range map[string]string{
		"doProviderHTTP":                 "providerEgressClient",
		"providerEgressClient":           "resolveProviderEgressIPs",
		"validateProviderEndpoint":       "ipProviderEgress",
		"ValidateProviderConfigEndpoint": "resolveProviderEgressIPs",
	} {
		if !funcCallsNamed(findFuncDeclInDir(t, dir, caller), callee) {
			t.Errorf("%s must use %s", caller, callee)
		}
	}
	if !funcCallsNamed(findFuncDeclInDir(t, dir, "providerEgressClient"), "egress.PinnedTransport") {
		t.Error("provider transport must pin resolved addresses")
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
