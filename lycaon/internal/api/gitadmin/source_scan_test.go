//go:build integration

package gitadmin

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// handlerSources lists this package's production files, split into the Git
// read/mutation handlers and the worktree handlers.
func handlerSources(t *testing.T) (git, worktree []string) {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	testutil.FailErr(t, "glob handler sources", err)
	for _, path := range paths {
		switch {
		case strings.HasSuffix(path, "_test.go"):
		case strings.HasPrefix(path, "worktree_"):
			worktree = append(worktree, path)
		default:
			git = append(git, path)
		}
	}
	if len(git) == 0 || len(worktree) == 0 {
		t.Fatalf("handler sources git=%v worktree=%v", git, worktree)
	}
	return git, worktree
}

func TestGitHandlersNoPrimaryRootPath(t *testing.T) {
	sources, _ := handlerSources(t)
	fset := token.NewFileSet()
	for _, path := range sources {
		src, err := os.ReadFile(path)
		testutil.FailErr(t, "read "+path, err)
		if strings.Contains(string(src), "PrimaryRootPath") {
			t.Fatalf("%s must not reference PrimaryRootPath", path)
		}
		if strings.Contains(string(src), "resolveProjectIDBody") {
			t.Fatalf("%s must not keep resolveProjectIDBody", path)
		}
		if _, err := parser.ParseFile(fset, path, src, 0); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
	}
}

func TestGitWorktreeNoForce(t *testing.T) {
	_, worktree := handlerSources(t)
	fset := token.NewFileSet()
	for _, path := range append(worktree, filepath.Join("..", "..", "git", "worktree.go")) {
		src, err := os.ReadFile(path)
		testutil.FailErr(t, "read "+path, err)
		if _, err := parser.ParseFile(fset, path, src, 0); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if strings.Contains(string(src), "--force") {
			t.Fatalf("%s contains --force", path)
		}
	}
}
