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

func TestBuildSessionWiresAuthzCapturer(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "app", "build_session.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read build_session.go: %v", err)
	}
	body := string(src)
	for _, want := range []string{
		"authzcontext.NewSQLCapturer",
		"SetAuthzSealer",
		"SetAuthzRecorder",
		"assertAuthzCapturer",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("build_session.go must reference %q", want)
		}
	}
}

func TestAuthzSealNoSilentNilSkip(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "session", "authz_seal.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse authz_seal.go: %v", err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		ifStmt, ok := n.(*ast.IfStmt)
		if !ok {
			return true
		}
		bin, ok := ifStmt.Cond.(*ast.BinaryExpr)
		if !ok || bin.Op != token.EQL {
			return true
		}
		left, ok := bin.X.(*ast.SelectorExpr)
		if !ok || left.Sel.Name != "authzSealer" {
			return true
		}
		right, ok := bin.Y.(*ast.Ident)
		if !ok || right.Name != "nil" {
			return true
		}
		if !ifStmtContainsIdent(ifStmt.Body, "authzSealRequired") {
			t.Fatal("authz_seal.go must not return nil on nil authzSealer without authzSealRequired guard")
		}
		return true
	})
}

func ifStmtContainsIdent(block ast.Node, name string) bool {
	found := false
	ast.Inspect(block, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if ok && id.Name == name {
			found = true
			return false
		}
		return true
	})
	return found
}

func TestAuthzSealFailureSurfacesViaNudge(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "session", "authz_seal.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read authz_seal.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "anchor.AuthzSealFailed") {
		t.Fatal("seal failure must emit the authz.seal_failed inform anchor")
	}
}
