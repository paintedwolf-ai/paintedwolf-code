package search

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/hitl"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/projectpaths"
)

func gitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, string(out))
	}
}

func initGitCheckout(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	seed(t, dir, files)
	gitCmd(t, dir, "init", "-q")
	gitCmd(t, dir, "add", ".")
	gitCmd(t, dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-qm", "fixture")
}

func installTreeGrants(t *testing.T, dirs ...string) {
	t.Helper()
	var canonical []string
	for _, dir := range dirs {
		resolved, err := filepath.EvalSymlinks(dir)
		if err != nil {
			resolved = filepath.Clean(dir)
		}
		canonical = append(canonical, resolved)
	}
	release := projectpaths.SetGrantedAccessSource(func(_, _, abs string, write bool) (projectpaths.Access, bool) {
		if write {
			return projectpaths.Access{}, false
		}
		for _, root := range canonical {
			rel, err := filepath.Rel(root, abs)
			if err == nil && (rel == "." || filepath.IsLocal(rel)) {
				return projectpaths.Access{Path: root, Tree: true}, true
			}
		}
		return projectpaths.Access{}, false
	})
	t.Cleanup(release)
}

func TestWorktreeIdentityMemoizesPerOperandRootWithoutGitSubprocesses(t *testing.T) {
	logs := discoveryCapture(t)
	h := newHarness(t, map[string]string{
		"internal/a.go": "package internal\n",
		"cmd/main.go":   "package main\n",
	})
	initGitCheckout(t, h.root, nil)

	extBase := t.TempDir()
	extBase, err := filepath.EvalSymlinks(extBase)
	testutil.FailErr(t, "resolve external base", err)

	wtBranch := filepath.Join(extBase, "wt-branch")
	wtDetached := filepath.Join(extBase, "wt-detached")
	plainClone := filepath.Join(extBase, "plain-clone")
	gitCmd(t, h.root, "worktree", "add", "-q", "-b", "feat/wt1", wtBranch)
	gitCmd(t, h.root, "worktree", "add", "-q", "--detach", wtDetached)
	gitCmd(t, h.root, "clone", "-q", h.root, plainClone)

	nestedRepo := filepath.Join(wtBranch, "vendor/nested")
	testutil.FailErr(t, "mkdir nested repo", os.MkdirAll(nestedRepo, 0o755))
	initGitCheckout(t, nestedRepo, map[string]string{"sub.go": "package sub\n"})

	installTreeGrants(t, wtBranch, wtDetached, plainClone)
	rootsBefore := slices.Clone(h.tc.Source.Roots)

	// Make Git unavailable on PATH during the walk to prove zero subprocesses run.
	emptyBin := t.TempDir()
	t.Setenv("PATH", emptyBin)

	h.cwd = wtBranch
	r := h.exec("", "find", "internal", "cmd", "vendor", "-name", "*.go")
	r.expect(t, "multi-operand sibling walk", "internal/a.go\ncmd/main.go\nvendor/nested/sub.go\n", "", 0)
	work := logs.records[r.execution]
	// One resolution for wtBranch across internal/cmd/vendor, plus one negative resolution at nestedRepo.
	if work["worktree_resolutions"] != 2 || work["worktree_matches"] != 1 {
		t.Fatalf("unexpected worktree memo counts: %v", work)
	}
	if !slices.Equal(h.tc.Source.Roots, rootsBefore) {
		t.Fatalf("source roots mutated: got %v want %v", h.tc.Source.Roots, rootsBefore)
	}

	h.cwd = "."
	detachedRun := h.exec("", "find", wtDetached, "-name", "a.go")
	detachedRun.expect(t, "detached worktree", filepath.Join(wtDetached, "internal/a.go")+"\n", "", 0)
	detachedWork := logs.records[detachedRun.execution]
	if detachedWork["worktree_resolutions"] != 1 || detachedWork["worktree_matches"] != 1 {
		t.Fatalf("detached resolution counts: %v", detachedWork)
	}

	cloneRun := h.exec("", "find", plainClone, "-name", "a.go")
	cloneRun.expect(t, "ordinary clone fallback", filepath.Join(plainClone, "internal/a.go")+"\n", "", 0)
	cloneWork := logs.records[cloneRun.execution]
	if cloneWork["worktree_resolutions"] != 1 || cloneWork["worktree_matches"] != 0 {
		t.Fatalf("clone resolution counts: %v", cloneWork)
	}
}

func TestWorktreeIdentityFileGrantNeverAdmitsParentListing(t *testing.T) {
	logs := discoveryCapture(t)
	h := newHarness(t, map[string]string{
		"pkg/allowed.go": "NeedleToken\n",
		"pkg/sibling.go": "NeedleToken\n",
	})
	initGitCheckout(t, h.root, nil)

	extBase, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve external base", err)
	wt := filepath.Join(extBase, "wt-file-grant")
	gitCmd(t, h.root, "worktree", "add", "-q", "-b", "feat/file-grant", wt)

	allowedFile := filepath.Join(wt, "pkg/allowed.go")
	h.tc.Files.ApprovedFileAccess = []hitl.GrantedPathDelta{{Path: allowedFile}}

	r := h.exec("", "grep", "NeedleToken", allowedFile)
	r.expect(t, "exact file grant read", "NeedleToken\n", "", 0)
	work := logs.records[r.execution]
	if work["worktree_resolutions"] != 0 || work["worktree_matches"] != 0 {
		t.Fatalf("file grant triggered directory worktree resolution: %v", work)
	}

	deniedDir := h.exec("", "find", filepath.Join(wt, "pkg"), "-name", "*.go")
	deniedDir.refused(t, "parent directory denied under file-only grant", "SURVEY_PATH_ESCAPE")
}

func TestWorktreeIdentityPartialRootAndMovedCheckout(t *testing.T) {
	logs := discoveryCapture(t)
	repoRoot, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve repo root", err)
	initGitCheckout(t, repoRoot, map[string]string{
		"packages/a/in.go":  "package a\n",
		"packages/b/out.go": "package b\n",
	})

	h := newHarness(t, nil)
	partialRoot := filepath.Join(repoRoot, "packages/a")
	h.root = partialRoot
	h.tc.Source.Roots = []projectroot.RootRef{{ID: "pkg-a", Label: "a", Path: partialRoot, IsPrimary: true}}
	h.tc.Source.ActiveRootID = "pkg-a"

	extBase, err := filepath.EvalSymlinks(t.TempDir())
	testutil.FailErr(t, "resolve external base", err)
	wt := filepath.Join(extBase, "wt-partial")
	moved := filepath.Join(extBase, "wt-moved")
	gitCmd(t, repoRoot, "worktree", "add", "-q", "-b", "feat/partial", wt)
	installTreeGrants(t, extBase)

	inScope := h.exec("", "find", filepath.Join(wt, "packages/a"), "-name", "*.go")
	inScope.expect(t, "mapped partial subtree", filepath.Join(wt, "packages/a/in.go")+"\n", "", 0)
	if logs.records[inScope.execution]["worktree_matches"] != 1 {
		t.Fatalf("expected positive checkout match: %v", logs.records[inScope.execution])
	}

	gitCmd(t, repoRoot, "worktree", "move", wt, moved)
	testutil.FailErr(t, "recreate plain directory at old pathname", os.MkdirAll(filepath.Join(wt, "packages/a"), 0o755))
	testutil.FailErr(t, "seed plain file at old pathname", os.WriteFile(filepath.Join(wt, "packages/a/plain.go"), []byte("package plain\n"), 0o644))

	oldPath := h.exec("", "find", filepath.Join(wt, "packages/a"), "-name", "*.go")
	oldPath.expect(t, "recreated old path falls back to live read", filepath.Join(wt, "packages/a/plain.go")+"\n", "", 0)
	if logs.records[oldPath.execution]["worktree_matches"] != 0 {
		t.Fatalf("recreated plain path matched worktree: %v", logs.records[oldPath.execution])
	}

	newPath := h.exec("", "find", filepath.Join(moved, "packages/a"), "-name", "*.go")
	newPath.expect(t, "moved worktree matches at new pathname", filepath.Join(moved, "packages/a/in.go")+"\n", "", 0)
	if logs.records[newPath.execution]["worktree_matches"] != 1 {
		t.Fatalf("moved worktree failed to match: %v", logs.records[newPath.execution])
	}
}
