package extpacks

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gitexec"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Git operations share hook and repository-state isolation.
func TestNoDirectGitExecInExtpacks(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	testutil.FailErr(t, "read package dir", err)
	var findings []string
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		testutil.FailErr(t, "read "+name, err)
		file, err := parser.ParseFile(fset, name, src, 0)
		testutil.FailErr(t, "parse "+name, err)
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			if isLiteralGitExecCall(call) {
				findings = append(findings, fset.Position(call.Pos()).String()+`: direct git exec; use gitexec.Run`)
			}
			return true
		})
	}
	if len(findings) > 0 {
		t.Fatalf("direct git exec remains:\n  %s", strings.Join(findings, "\n  "))
	}
}

func isLiteralGitExecCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext" {
		return false
	}
	args := call.Args
	if sel.Sel.Name == "CommandContext" && len(args) >= 2 {
		return isGitBinaryLit(args[1])
	}
	if len(args) >= 1 {
		return isGitBinaryLit(args[0])
	}
	return false
}

func isGitBinaryLit(expr ast.Expr) bool {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return false
	}
	v, err := strconv.Unquote(lit.Value)
	if err != nil {
		return false
	}
	return v == "git" || v == "git-lfs"
}

func TestExtpacksInstallDoesNotRunHooks(t *testing.T) {
	origin := t.TempDir()
	out, code, err := gitexec.Run(t.Context(), origin, []string{"init"}, gitexec.Opts{})
	if err != nil || code != 0 {
		t.Fatalf("init origin: err=%v code=%d out=%s", err, code, out)
	}
	hookDir := filepath.Join(origin, ".git", "hooks")
	testutil.FailErr(t, "mkdir hooks", os.MkdirAll(hookDir, 0o755))
	hook := filepath.Join(hookDir, "post-checkout")
	sentinel := filepath.Join(t.TempDir(), "post-checkout-ran")
	script := "#!/bin/sh\necho ran > " + sentinel + "\n"
	testutil.FailErr(t, "write hook", os.WriteFile(hook, []byte(script), 0o755))
	testutil.FailErr(t, "write readme", os.WriteFile(filepath.Join(origin, "README.md"), []byte("pack\n"), 0o644))
	_, code, err = gitexec.Run(t.Context(), origin, []string{"add", "-A"}, gitexec.Opts{})
	if err != nil || code != 0 {
		t.Fatalf("add: err=%v code=%d", err, code)
	}
	_, code, err = gitexec.Run(t.Context(), origin, []string{"commit", "-m", "init"}, gitexec.Opts{
		Identity: &gitexec.Identity{Name: "t", Email: "t@t"},
	})
	if err != nil || code != 0 {
		t.Fatalf("commit: err=%v code=%d", err, code)
	}

	dest := filepath.Join(t.TempDir(), "clone")
	if err := gitClone(context.Background(), origin, dest, "HEAD"); err != nil {
		t.Fatalf("gitClone: %v", err)
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Fatal("post-checkout hook ran during pack clone")
	}
}
