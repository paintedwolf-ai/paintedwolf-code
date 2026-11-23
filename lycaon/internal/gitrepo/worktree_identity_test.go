package gitrepo_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/gitrepo"
	"github.com/lycaon/lycaon/internal/testutil"
)

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, string(out))
	}
}

func initGitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	root := canonicalTempDir(t)
	runGit(t, root, "init", "-q")
	for rel, content := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		testutil.FailErr(t, "mkdir parent "+rel, os.MkdirAll(filepath.Dir(full), 0o755))
		testutil.FailErr(t, "write "+rel, os.WriteFile(full, []byte(content), 0o644))
	}
	runGit(t, root, "add", ".")
	runGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "initial")
	return root
}

func rewriteWorktreeRelativePointers(t *testing.T, mainRepo, wt string) {
	t.Helper()
	adminDir := filepath.Join(mainRepo, ".git", "worktrees", filepath.Base(wt))
	relToAdmin, err := filepath.Rel(wt, adminDir)
	testutil.FailErr(t, "rel to admin", err)
	relToDotGit, err := filepath.Rel(adminDir, filepath.Join(wt, ".git"))
	testutil.FailErr(t, "rel to dotgit", err)
	testutil.FailErr(t, "write relative .git", os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+relToAdmin+"\n"), 0o644))
	testutil.FailErr(t, "write relative gitdir", os.WriteFile(filepath.Join(adminDir, "gitdir"), []byte(relToDotGit+"\n"), 0o644))
}

func TestCheckoutResolverVerifiesReciprocalLinkedWorktrees(t *testing.T) {
	mainRepo := initGitRepo(t, map[string]string{
		"packages/a/a.go": "package a\n",
		"packages/b/b.go": "package b\n",
	})
	sandboxDir := canonicalTempDir(t)
	withSpaces := filepath.Join(sandboxDir, "branch with spaces ")
	detached := filepath.Join(sandboxDir, "detached")
	runGit(t, mainRepo, "worktree", "add", "-q", "-b", "feat/spaces", withSpaces)
	runGit(t, mainRepo, "worktree", "add", "-q", "--detach", detached)
	rewriteWorktreeRelativePointers(t, mainRepo, detached)

	resolver := gitrepo.NewCheckoutResolver([]gitrepo.AttachedRoot{{ID: "primary", Path: mainRepo}})

	for _, wt := range []string{withSpaces, detached} {
		binding, ok := resolver.Resolve(wt, wt)
		if !ok {
			t.Fatalf("expected valid linked worktree for %q", wt)
		}
		addr, ok := binding.Project(filepath.Join(wt, "packages/a/a.go"))
		if !ok || addr.RootID != "primary" || addr.RootPath != wt || addr.Rel != "packages/a/a.go" {
			t.Fatalf("unexpected projection for %q: %+v ok=%v", wt, addr, ok)
		}
	}

	relocated := filepath.Join(sandboxDir, "relocated")
	runGit(t, mainRepo, "worktree", "move", withSpaces, relocated)
	if _, ok := resolver.Resolve(withSpaces, withSpaces); ok {
		t.Fatal("removed source pathname still resolved")
	}
	if _, ok := resolver.Resolve(relocated, relocated); !ok {
		t.Fatal("relocated worktree failed reciprocal verification")
	}

	impostor := filepath.Join(sandboxDir, "copied-marker")
	testutil.FailErr(t, "mkdir impostor", os.MkdirAll(impostor, 0o755))
	markerBytes, err := os.ReadFile(filepath.Join(relocated, ".git"))
	testutil.FailErr(t, "read marker", err)
	testutil.FailErr(t, "copy marker", os.WriteFile(filepath.Join(impostor, ".git"), markerBytes, 0o644))
	if _, ok := resolver.Resolve(impostor, impostor); ok {
		t.Fatal("copied .git marker without reverse registration matched")
	}

	cloned := filepath.Join(sandboxDir, "clone")
	runGit(t, mainRepo, "clone", "-q", mainRepo, cloned)
	if _, ok := resolver.Resolve(cloned, cloned); ok {
		t.Fatal("ordinary clone matched linked worktree check")
	}

	runGit(t, mainRepo, "worktree", "remove", "--force", detached)
	testutil.FailErr(t, "recreate removed worktree dir", os.MkdirAll(detached, 0o755))
	if _, ok := resolver.Resolve(detached, detached); ok {
		t.Fatal("recreated plain directory at former worktree path matched")
	}
}

func TestCheckoutResolverHandlesAttachedLinkedWorktreeAndPartialSubtrees(t *testing.T) {
	mainRepo := initGitRepo(t, map[string]string{
		"packages/a/src/file.go": "package src\n",
		"packages/b/other.go":    "package b\n",
	})
	sandboxDir := canonicalTempDir(t)
	attachedWT := filepath.Join(sandboxDir, "attached-wt")
	siblingWT := filepath.Join(sandboxDir, "sibling-wt")
	runGit(t, mainRepo, "worktree", "add", "-q", "-b", "attached", attachedWT)
	runGit(t, mainRepo, "worktree", "add", "-q", "-b", "sibling", siblingWT)

	partialAttached := filepath.Join(attachedWT, "packages/a")
	resolver := gitrepo.NewCheckoutResolver([]gitrepo.AttachedRoot{{ID: "pkg-a", Path: partialAttached}})

	binding, ok := resolver.Resolve(siblingWT, siblingWT)
	if !ok {
		t.Fatal("sibling worktree did not match attached linked worktree")
	}
	if _, ok := binding.Project(filepath.Join(siblingWT, "packages/b/other.go")); ok {
		t.Fatal("outside attached partial root projected into catalog")
	}
	wantBase := filepath.Join(siblingWT, "packages/a")
	addr, ok := binding.Project(filepath.Join(siblingWT, "packages/a/src/file.go"))
	if !ok || addr.RootID != "pkg-a" || addr.RootPath != wantBase || addr.Rel != "src/file.go" {
		t.Fatalf("unexpected partial-root projection: %+v ok=%v", addr, ok)
	}

	narrowGrant := filepath.Join(siblingWT, "packages/a/src")
	narrowBinding, ok := resolver.Resolve(narrowGrant, narrowGrant)
	if !ok {
		t.Fatal("narrow subtree grant inside registered worktree did not resolve via trusted registration")
	}
	narrowAddr, ok := narrowBinding.Project(filepath.Join(narrowGrant, "file.go"))
	if !ok || narrowAddr.RootPath != narrowGrant || narrowAddr.Rel != "file.go" {
		t.Fatalf("unexpected narrow-grant projection: %+v ok=%v", narrowAddr, ok)
	}
}

func TestCheckoutResolverRejectsTruncatedAndAmbiguousMetadata(t *testing.T) {
	mainRepo := initGitRepo(t, map[string]string{"a.go": "package main\n"})
	sandboxDir := canonicalTempDir(t)
	wt := filepath.Join(sandboxDir, "wt")
	runGit(t, mainRepo, "worktree", "add", "-q", "-b", "wt-branch", wt)

	ambiguous := gitrepo.NewCheckoutResolver([]gitrepo.AttachedRoot{
		{ID: "r1", Path: mainRepo},
		{ID: "r2", Path: wt},
	})
	wt2 := filepath.Join(sandboxDir, "wt2")
	runGit(t, mainRepo, "worktree", "add", "-q", "-b", "wt-branch-2", wt2)
	if binding, ok := ambiguous.Resolve(wt2, wt2); ok {
		if _, projected := binding.Project(wt2); projected {
			t.Fatal("ambiguous equal-specificity attached roots projected")
		}
	}

	oversized := filepath.Join(sandboxDir, "oversized")
	testutil.FailErr(t, "mkdir oversized", os.MkdirAll(oversized, 0o755))
	longLine := "gitdir: " + strings.Repeat("a", 9000) + "\n"
	testutil.FailErr(t, "write oversized .git", os.WriteFile(filepath.Join(oversized, ".git"), []byte(longLine), 0o644))
	single := gitrepo.NewCheckoutResolver([]gitrepo.AttachedRoot{{ID: "r1", Path: mainRepo}})
	if _, ok := single.Resolve(oversized, oversized); ok {
		t.Fatal("oversized pointer file was accepted")
	}
}
