package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
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
