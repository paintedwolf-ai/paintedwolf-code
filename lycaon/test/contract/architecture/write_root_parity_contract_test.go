package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestWriteRootChokePointsPinned(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	checks := []struct {
		rel  string
		want string
	}{
		{filepath.Join("lycaon", "internal", "confine", "write_root_validation.go"), "AttachedWriteRootRefused"},
		{filepath.Join("lycaon", "internal", "tools", "confine_roots.go"), "ValidateAttachedWriteRoots"},
		{filepath.Join("lycaon", "internal", "session", "sandbox_write_root_broker.go"), "ControlPlanePathDenied"},
		// The grant door and the boundary read one granted-lane predicate: what
		// the broker grants as a plain root, prepareRequest applies.
		{filepath.Join("lycaon", "internal", "session", "sandbox_write_root_broker.go"), "GrantedWriteRootRefused"},
		{filepath.Join("lycaon", "internal", "confine", "request.go"), "ValidateGrantedWriteRoots"},
		{filepath.Join("lycaon", "internal", "confine", "write_root_grants.go"), "func AttachedWriteRootRefused"},
		{filepath.Join("lycaon", "internal", "confine", "write_root_grants.go"), "func GrantedWriteRootRefused"},
	}
	for _, c := range checks {
		raw, err := os.ReadFile(filepath.Join(root, c.rel))
		contractcheck.FailErr(t, "read "+c.rel, err)
		if !strings.Contains(string(raw), c.want) {
			t.Errorf("%s must contain %q", c.rel, c.want)
		}
	}
}

// TestConfineRootsForActionTakesOnlyToolContext enforces a ToolContext-only input.
func TestConfineRootsForActionTakesOnlyToolContext(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "tools", "confine_roots.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	contractcheck.FailErr(t, "parse confine_roots.go", err)

	var checked bool
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name == nil || fn.Name.Name != "ConfineRootsForAction" {
			continue
		}
		checked = true
		params := fn.Type.Params
		if params == nil || len(params.List) != 1 {
			t.Fatalf("ConfineRootsForAction must take exactly one parameter (a ToolContext); got %d", len(params.List))
		}
		ident, ok := params.List[0].Type.(*ast.Ident)
		if !ok || ident.Name != "ToolContext" {
			t.Fatalf("ConfineRootsForAction parameter must be ToolContext, got %s",
				types.ExprString(params.List[0].Type))
		}
	}
	if !checked {
		t.Fatal("ConfineRootsForAction not found in internal/tools/confine_roots.go")
	}
}
