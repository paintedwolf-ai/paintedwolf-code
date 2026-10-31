package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// Every native source-content surface has an attributed delivery seam.
func TestNativeCredentialContentDeliveryInventory(t *testing.T) {
	surfaces := []struct{ tool, file, function, delivery string }{
		{"read", "internal/tools/native/survey/read.go", "Run", "ObserveCredentialDelivery"},
		{"grep", "internal/tools/native/survey/grep.go", "finalizeGrepResponse", "deliverGrepCredentials"},
		{"diff", "internal/tools/native/survey/diff.go", "Run", "deliverCredentialText"},
		{"summarize", "internal/tools/native/survey/summarize.go", "Summarize", "deliverSummaryCredentials"},
		{"summarize structured entry", "internal/tools/native/survey/summarize.go", "Run", "Summarize"},
		{"summarize command entry", "internal/tools/native/literal/domain.go", "runSummarize", "Summarize"},
		{"git_show", "internal/tools/native/git_introspection.go", "Run", "deliverGitPatchCredentials"},
		{"git_blame", "internal/tools/native/git_introspection.go", "Run", "ObserveCredentialDelivery"},
	}
	for _, surface := range surfaces {
		t.Run(surface.tool, func(t *testing.T) {
			path := filepath.Join("..", "..", "..", filepath.FromSlash(surface.file))
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", path, err)
			}
			found := false
			for _, declaration := range file.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Name.Name != surface.function {
					continue
				}
				ast.Inspect(function.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					switch name := call.Fun.(type) {
					case *ast.Ident:
						found = found || name.Name == surface.delivery
					case *ast.SelectorExpr:
						found = found || name.Sel.Name == surface.delivery
					}
					return true
				})
			}
			if !found {
				t.Fatalf("%s lacks %s in %s", surface.tool, surface.delivery, surface.function)
			}
		})
	}
}
