package contract

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	contractcheck "github.com/lycaon/lycaon/test/contract/internal/check"
)

// modeTransitionPushSiteRegistry lists production paths that must push ModeTransitionCause.
var modeTransitionPushSiteRegistry = []string{
	"lycaon/internal/app/build_delegation.go",
}

func TestModeTransitionPushSiteRegistry(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	for _, rel := range modeTransitionPushSiteRegistry {
		path := filepath.Join(root, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if !strings.Contains(string(data), "PushExecutionModeTransitionCause") {
			t.Fatalf("%s must call PushExecutionModeTransitionCause", rel)
		}
	}
	wireText := contractcheck.ServeWireSource(t)
	if !strings.Contains(wireText, "PushExecutionModeTransitionCause") {
		t.Fatal("serve build graph must call PushExecutionModeTransitionCause")
	}
}

func TestPushModeTransitionCauseAPIExists(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "coordinator", "runtime.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse runtime.go: %v", err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Name == nil {
			return true
		}
		if fn.Name.Name == "PushModeTransitionCause" {
			found = true
		}
		return true
	})
	if !found {
		t.Fatal("Runtime.PushModeTransitionCause must exist for workflow push sites")
	}
}
