package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

func TestApplyRefusalFactsDoesNotMarkError(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "tools", "refusal_facts.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	testutil.FailErr(t, "parse refusal_facts.go", err)

	ast.Inspect(f, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if sel.Sel.Name != "ToolResultOutcomeError" && sel.Sel.Name != "ToolResultOutcomeRejected" {
			return true
		}
		t.Errorf("refusal_facts.go:%d: %s — settled confine refusals stay completed",
			fset.Position(sel.Pos()).Line, sel.Sel.Name)
		return true
	})
}

func TestStampRefusalFillsACodeWhenAttributed(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "confine", "refusal_stamp.go")
	src, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ParseComments)
	testutil.FailErr(t, "parse refusal_stamp.go", err)
	found := false
	ast.Inspect(src, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if ok && ident.Name == "CodeBoundaryRefused" {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("StampRefusal missing CodeBoundaryRefused")
	}
}

func TestSandboxPostInvokeCodesCoverBrokerRefusalsAndVerify(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "session", "policyfacts", "oar_observe.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	testutil.FailErr(t, "parse oar_observe.go", err)

	want := map[string]bool{
		"CodeRemotePackageDestinationDenied": false,
		"CodeBoundaryRefused":                false,
		"VerifyUnverifiableCode":             false,
	}
	ast.Inspect(f, func(n ast.Node) bool {
		ident, ok := n.(*ast.Ident)
		if !ok {
			return true
		}
		if _, ok := want[ident.Name]; ok {
			want[ident.Name] = true
		}
		return true
	})
	for name, found := range want {
		if !found {
			t.Errorf("sandboxPostInvokeCodes missing %s", name)
		}
	}
}

func TestCatalogToolProducersDoNotStateUncodedErrorOrRejected(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	toolsDir := filepath.Join(root, "lycaon", "internal", "tools")
	err := filepath.WalkDir(toolsDir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			t.Errorf("parse %s: %v", path, parseErr)
			return nil
		}
		ast.Inspect(f, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name != "ToolResultOutcomeError" && sel.Sel.Name != "ToolResultOutcomeRejected" {
				return true
			}
			rel, _ := filepath.Rel(root, path)
			t.Errorf("%s:%d: catalog tools stated %s", rel, fset.Position(sel.Pos()).Line, sel.Sel.Name)
			return true
		})
		return nil
	})
	testutil.FailErr(t, "walk internal/tools", err)
}
