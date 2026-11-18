package contract

import (
	"go/ast"
	"path/filepath"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestNoSessionConfigTypeInSessionPackage(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	sessionDir := filepath.Join(root, "lycaon", "internal", "session")
	fset, files := contractcheck.ParseNonTestGoTree(t, sessionDir)
	for _, f := range files {
		// The rule covers package session, not its subpackages.
		if f.Name.Name != "session" {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			ident, ok := n.(*ast.Ident)
			if !ok || ident.Name != "Config" {
				return true
			}
			if ident.Obj != nil && ident.Obj.Kind == ast.Typ {
				t.Fatalf("session package must not define Config type in %s", fset.Position(ident.Pos()).Filename)
			}
			return true
		})
	}
}
