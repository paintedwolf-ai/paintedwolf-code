package contract

import (
	"go/ast"
	"path/filepath"
	"strings"
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

func findFuncDeclInDir(t *testing.T, dir, name string) *ast.FuncDecl {
	t.Helper()
	_, files := contractcheck.ParseNonTestGoTree(t, dir)
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name == nil || fn.Name.Name != name || fn.Body == nil {
				continue
			}
			return fn
		}
	}
	t.Fatalf("function %s not found in %s", name, dir)
	return nil
}

func funcCallsNamed(fn *ast.FuncDecl, name string) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		got := contractcheck.CallName(call)
		if got == name || strings.HasSuffix(got, "."+name) {
			found = true
		}
		return true
	})
	return found
}
