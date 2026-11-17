package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// A nil Live callback permits scheduled warming without an active app.
func TestWarmRunnerWiredWithPresenceGate(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	relFile := "internal/app/boards/build.go"
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join(root, "lycaon", relFile), nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse %s: %v", relFile, err)
	}
	var found, gated bool
	ast.Inspect(file, func(n ast.Node) bool {
		cl, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		sel, ok := cl.Type.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "WarmRunner" {
			return true
		}
		found = true
		for _, elt := range cl.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Live" {
				gated = true
			}
		}
		return true
	})
	if !found {
		t.Fatalf("%s: no webresearch.WarmRunner literal found", relFile)
	}
	if !gated {
		t.Fatalf("%s: WarmRunner must set Live — scheduled warming runs only while the app is in use", relFile)
	}
}
