package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Guards the fail-closed / exact-socket / direct-IP emitter contracts for the
// non-HTTP capability boundary. Structural AST checks so comment edits do not fail CI.

func TestContractNoAmbientNetworkAllowAlias(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	src := contractcheck.ReadRepoFile(t, root, "lycaon/internal/confine/confine.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "confine.go", src, 0)
	contractcheck.FailErr(t, "parse confine.go", err)
	fn := findNamedFunc(file, "DefaultConfinement")
	if fn == nil {
		t.Fatal("DefaultConfinement missing")
	}
	body := src[fn.Body.Pos()-1 : fn.Body.End()]
	for _, alias := range []string{`case "allow"`, `case "unrestricted"`, `case "off"`} {
		if strings.Contains(body, alias) {
			t.Fatalf("DefaultConfinement must not special-case ambient alias %s", alias)
		}
	}
}

func TestContractSocketEmitterForbidsBroadPredicates(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	src := contractcheck.ReadRepoFile(t, root, "lycaon/internal/confine/network_profile.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "network_profile.go", src, 0)
	contractcheck.FailErr(t, "parse network_profile.go", err)
	fn := findNamedFunc(file, "writeSocketGrantRules")
	if fn == nil {
		t.Fatal("writeSocketGrantRules missing")
	}
	hasLiteral := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		for _, banned := range []string{"subpath", "regex", "network*"} {
			if strings.Contains(lit.Value, banned) {
				t.Fatalf("writeSocketGrantRules must not emit %q", banned)
			}
		}
		if strings.Contains(lit.Value, "literal") {
			hasLiteral = true
		}
		return true
	})
	if !hasLiteral {
		t.Fatal("writeSocketGrantRules must emit literal predicates")
	}

	netFn := findNamedFunc(file, "writeNetworkRules")
	if netFn == nil {
		t.Fatal("writeNetworkRules missing")
	}
	ast.Inspect(netFn.Body, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if strings.Contains(lit.Value, "(allow network*)") {
			t.Fatal("writeNetworkRules must not emit blanket network*")
		}
		return true
	})
}

func TestContractEgressContainedExcludesDirectIP(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	src := contractcheck.ReadRepoFile(t, root, "lycaon/internal/settings/reversibility.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "reversibility.go", src, 0)
	contractcheck.FailErr(t, "parse reversibility.go", err)
	fn := findNamedFunc(file, "boundaryHolds")
	if fn == nil {
		t.Fatal("boundaryHolds missing")
	}
	body := src[fn.Body.Pos()-1 : fn.Body.End()]
	if strings.Contains(body, "DirectIP") || strings.Contains(body, "direct_ip") {
		t.Fatal("boundaryHolds must not treat direct_ip as a held boundary")
	}
	if !strings.Contains(body, "ContainedEgressDeny") || !strings.Contains(body, "ContainedEgressProxy") {
		t.Fatal("egressContained must keep deny|proxy polarity")
	}
}

func findNamedFunc(file *ast.File, name string) *ast.FuncDecl {
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if ok && fn.Name != nil && fn.Name.Name == name {
			return fn
		}
	}
	return nil
}
