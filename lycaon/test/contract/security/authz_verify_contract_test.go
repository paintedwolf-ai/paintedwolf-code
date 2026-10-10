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
	for path, wants := range map[string][]string{
		"app/security/authorization.go": {"authzcontext.NewSQLCapturer"},
		"app/sessions/build.go":         {"Runner.Authorization.SetSealer"},
		"app/sessions/checkpoints.go":   {"SetAuthzRecorder"},
		"app/build_session.go":          {"assertAuthzCapturer", "Runner.Authorization.Wired"},
	} {
		src, err := os.ReadFile(filepath.Join(root, "lycaon", "internal", path))
		contractcheck.FailErr(t, "read "+path, err)
		for _, want := range wants {
			if !strings.Contains(string(src), want) {
				t.Fatalf("%s must reference %q", path, want)
			}
		}
	}
}

func TestAuthzSealNoSilentNilSkip(t *testing.T) {
	t.Parallel()
	root := contractcheck.RepoRoot(t)
	path := filepath.Join(root, "lycaon", "internal", "session", "authorization", "service.go")
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse authorization/service.go: %v", err)
	}
	foundGuard := false
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
		foundGuard = true
		if !ifStmtContainsIdent(ifStmt.Body, "authzSealRequired") {
			t.Fatal("authorization/service.go must not return nil on nil authzSealer without authzSealRequired guard")
		}
		return true
	})
	if !foundGuard {
		t.Fatal("authorization.Seal is missing its nil-sealer guard")
	}
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
	path := filepath.Join(root, "lycaon", "internal", "session", "turnexecution", "run.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read turnexecution/run.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "anchor.AuthzSealFailed") {
		t.Fatal("seal failure must emit the authz.seal_failed inform anchor")
	}
}
