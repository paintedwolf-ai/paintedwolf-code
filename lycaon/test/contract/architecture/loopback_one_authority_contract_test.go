package contract

import (
	"go/ast"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Loopback decided from a host string has one authority: internal/egress.
// Deciding it from a resolved netip.Addr is the floor and needs none.
var loopbackExemptions = map[string]string{
	"internal/httpclient/proxy.go": "proxy bypass: also treats the RFC 6761 `*.localhost` names as local, which suits " +
		"skipping a proxy and not deciding what to bind",
	"internal/projectstack/doclinks.go": "broader question: whether a documentation URL is public, covering private, " +
		"unspecified and link-local ranges too",
}

func TestLoopbackHostIdentityHasOneAuthority(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corp, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon"))
	contractcheck.FailErr(t, "load AST corpus", err)

	unused := map[string]bool{}
	for rel := range loopbackExemptions {
		unused[rel] = true
	}

	var offences []string
	for _, gf := range corp.Files() {
		if gf.IsTest {
			continue
		}
		rel := filepath.ToSlash(gf.Rel)
		if strings.HasPrefix(rel, "internal/egress/") {
			continue // the authority itself
		}
		if !fileDecidesLoopbackFromAString(gf.AST) {
			continue
		}
		if _, exempt := loopbackExemptions[rel]; exempt {
			delete(unused, rel)
			continue
		}
		offences = append(offences, rel)
	}
	sort.Strings(offences)

	if len(offences) > 0 {
		t.Errorf("loopback is decided from a host string outside the authority:\n  %s\n\n"+
			"Use egress.SyntacticLoopback for a name or literal, egress.LoopbackLiteral for an "+
			"address literal only. Deciding from a resolved netip.Addr needs no authority — that "+
			"is the floor, and it is what ResolveIPsWithPolicy already does.",
			strings.Join(offences, "\n  "))
	}
	if len(unused) > 0 {
		var stale []string
		for rel := range unused {
			stale = append(stale, rel)
		}
		sort.Strings(stale)
		t.Errorf("these exemptions no longer match anything and should be deleted:\n  %s",
			strings.Join(stale, "\n  "))
	}
}

// fileDecidesLoopbackFromAString finds predicates that infer loopback from a name.
// Predicates over resolved addresses are outside this rule.
func fileDecidesLoopbackFromAString(f *ast.File) bool {
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		if found {
			return false
		}
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil || !takesAName(fn) || !returnsBool(fn) {
			return true
		}
		if bodyDecidesLoopback(fn.Body) {
			found = true
			return false
		}
		return true
	})
	return found
}

// takesAName reports a parameter that is a spelling rather than a resolved
// address: a bare string, or a URL, which is a string with structure.
func takesAName(fn *ast.FuncDecl) bool {
	if fn.Type.Params == nil {
		return false
	}
	for _, field := range fn.Type.Params.List {
		switch t := field.Type.(type) {
		case *ast.Ident:
			if t.Name == "string" {
				return true
			}
		case *ast.StarExpr:
			if sel, ok := t.X.(*ast.SelectorExpr); ok && sel.Sel.Name == "URL" {
				return true
			}
		}
	}
	return false
}

func returnsBool(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil {
		return false
	}
	for _, field := range fn.Type.Results.List {
		if id, ok := field.Type.(*ast.Ident); ok && id.Name == "bool" {
			return true
		}
	}
	return false
}

// bodyDecidesLoopback looks for the reserved name used in a comparison, or a
// parse-then-IsLoopback pair. A bare mention of the word is not enough.
func bodyDecidesLoopback(body *ast.BlockStmt) bool {
	comparesName, parses, checksLoopback := false, false, false
	isLocalhost := func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		return ok && strings.EqualFold(strings.Trim(lit.Value, `\"`), "localhost")
	}
	ast.Inspect(body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.BinaryExpr:
			if node.Op.String() == "==" && (isLocalhost(node.X) || isLocalhost(node.Y)) {
				comparesName = true
			}
		case *ast.CaseClause:
			for _, expr := range node.List {
				if isLocalhost(expr) {
					comparesName = true
				}
			}
		case *ast.CallExpr:
			if sel, ok := node.Fun.(*ast.SelectorExpr); ok {
				switch sel.Sel.Name {
				case "ParseIP", "ParseAddr":
					parses = true
				case "IsLoopback":
					checksLoopback = true
				case "EqualFold":
					for _, arg := range node.Args {
						if isLocalhost(arg) {
							comparesName = true
						}
					}
				}
			}
		}
		return true
	})
	return comparesName || (parses && checksLoopback)
}

// TestLoopbackAuthorityIsReachable requires both canonical predicates.
func TestLoopbackAuthorityIsReachable(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	corp, err := contractcheck.LoadGoASTCorpus(filepath.Join(root, "lycaon", "internal", "egress"))
	contractcheck.FailErr(t, "load egress corpus", err)

	want := map[string]bool{"LoopbackLiteral": false, "SyntacticLoopback": false}
	for _, gf := range corp.Files() {
		for _, decl := range gf.AST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if ok && fn.Recv == nil {
				if _, tracked := want[fn.Name.Name]; tracked {
					want[fn.Name.Name] = true
				}
			}
		}
	}
	for name, seen := range want {
		if !seen {
			t.Fatalf("internal/egress no longer exports %s; "+
				"TestLoopbackHostIdentityHasOneAuthority names it as the replacement", name)
		}
	}
}
