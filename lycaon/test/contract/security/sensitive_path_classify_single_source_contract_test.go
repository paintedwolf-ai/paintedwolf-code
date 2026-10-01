package contract

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// Direct matching is limited to catalog internals and canonicalized settings.
var sensitivePathClassifySkipFiles = map[string]bool{
	"lycaon/internal/sensitivepath/sensitivepath.go": true,
	"lycaon/internal/settings/granted_path_offer.go": true,
}

func TestSensitivePathClassifyHasOneSource(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	var offences []string

	for _, tree := range []string{"lycaon/internal"} {
		err := filepath.WalkDir(filepath.Join(root, filepath.FromSlash(tree)), func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				if entry.Name() == "testdata" || strings.HasPrefix(entry.Name(), ".") {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			rel = filepath.ToSlash(rel)
			if sensitivePathClassifySkipFiles[rel] {
				return nil
			}

			fset := token.NewFileSet()
			file, parseErr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
			if parseErr != nil {
				return parseErr
			}

			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if sel.Sel.Name != "Match" && sel.Sel.Name != "MatchCovering" {
					return true
				}
				if !receiverLooksLikeLocationsCatalog(sel.X) {
					return true
				}
				offences = append(offences, fmt.Sprintf("%s:%d", rel, fset.Position(call.Pos()).Line))
				return true
			})
			return nil
		})
		contractcheck.FailErr(t, "walk "+tree, err)
	}

	if len(offences) > 0 {
		t.Fatalf("direct Catalog.Match/MatchCovering call bypasses symlink resolution — "+
			"use (*sensitivepath.Catalog).ClassifyResolved instead:\n  %s",
			strings.Join(offences, "\n  "))
	}
}

// receiverLooksLikeLocationsCatalog recognizes the standard catalog field.
func receiverLooksLikeLocationsCatalog(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	return sel.Sel.Name == "Locations"
}
