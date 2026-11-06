package workspace_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sandbox"
	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
)

func TestCreateWorkerWorkspaceCapturesCompleteImmutableSnapshot(t *testing.T) {
	primary := t.TempDir()
	for _, rel := range []string{".git/HEAD", settingsoverlay.Rel("blueprints/x.md"), "main.go", "pkg/a.go"} {
		p := filepath.Join(primary, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			testutil.FailErr(t, "mkdir", err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			testutil.FailErr(t, "write", err)
		}
	}
	mgr := workspace.NewManager(t.TempDir(), t.TempDir())
	binding, err := mgr.CreateWorkerWorkspace(context.Background(), primary, "skip")
	testutil.FailErr(t, "CreateWorkerWorkspace", err)
	defer func() { _ = mgr.DestroyWorkerWorkspace(t.Context(), binding) }()

	if _, err := os.Stat(filepath.Join(binding.Root, "main.go")); err != nil {
		t.Fatalf("claimed snapshot missing main.go: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binding.Root, "pkg", "a.go")); err != nil {
		t.Fatalf("claimed snapshot missing pkg/a.go: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binding.Root, ".git")); !os.IsNotExist(err) {
		t.Fatal("claimed snapshot must exclude repository metadata")
	}
	meta, err := workspace.LoadJobMeta(enginepaths.MetaDirForBranchRoot(binding.Root))
	testutil.FailErr(t, "LoadJobMeta", err)
	if !meta.SnapshotComplete {
		t.Fatal("claim must mark its immutable snapshot complete")
	}
	if len(meta.Roots) != 1 || meta.Roots[0].Path != filepath.Clean(primary) {
		t.Fatalf("roots=%v", meta.Roots)
	}
}

func TestOverlaySurveyListsCompleteSnapshot(t *testing.T) {
	primary := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(primary, "seen.go"), []byte("ok"), 0o644))
	mgr := workspace.NewManager(t.TempDir(), t.TempDir())
	binding, err := mgr.CreateWorkerWorkspace(context.Background(), primary, "survey")
	testutil.FailErr(t, "CreateWorkerWorkspace", err)
	defer func() { _ = mgr.DestroyWorkerWorkspace(t.Context(), binding) }()

	entries, err := workspace.OverlaySurveyReadDir(binding.Root, "", sandbox.SurveyOptions{IncludeHidden: true})
	testutil.FailErr(t, "OverlaySurveyReadDir", err)
	names := map[string]bool{}
	for _, e := range entries {
		names[e.DirEntry.Name()] = true
	}
	if !names["seen.go"] {
		t.Fatalf("entries=%v want seen.go", names)
	}
	if body, err := os.ReadFile(filepath.Join(binding.Root, "seen.go")); err != nil || string(body) != "ok" {
		t.Fatalf("snapshot body = %q err=%v", body, err)
	}
	testutil.FailErr(t, "write later canonical file", os.WriteFile(filepath.Join(primary, "later.go"), []byte("later"), 0o644))
	entries, err = workspace.OverlaySurveyReadDir(binding.Root, "", sandbox.SurveyOptions{IncludeHidden: true})
	testutil.FailErr(t, "OverlaySurveyReadDir immutable", err)
	for _, entry := range entries {
		if entry.DirEntry.Name() == "later.go" {
			t.Fatal("complete branch survey leaked a later canonical path")
		}
	}

	testutil.FailErr(t, "mkdir branch-only", os.WriteFile(filepath.Join(binding.Root, "branch_only.go"), []byte("b"), 0o644))
	entries, err = workspace.OverlaySurveyReadDir(binding.Root, "", sandbox.SurveyOptions{IncludeHidden: true})
	testutil.FailErr(t, "OverlaySurveyReadDir2", err)
	names = map[string]bool{}
	for _, e := range entries {
		names[e.DirEntry.Name()] = true
	}
	if !names["branch_only.go"] || !names["seen.go"] {
		t.Fatalf("entries=%v", names)
	}

	testutil.FailErr(t, "remove branch file", os.Remove(filepath.Join(binding.Root, "seen.go")))
	entries, err = workspace.OverlaySurveyReadDir(binding.Root, "", sandbox.SurveyOptions{IncludeHidden: true})
	testutil.FailErr(t, "OverlaySurveyReadDir after removal", err)
	for _, e := range entries {
		if e.DirEntry.Name() == "seen.go" {
			t.Fatal("removed branch file remained in survey")
		}
	}
}

func TestOverlaySurveyWalkRejectsMissingBranch(t *testing.T) {
	err := workspace.OverlaySurveyWalk(context.Background(), filepath.Join(t.TempDir(), "missing"), sandbox.SurveyOptions{},
		func(sandbox.SurveyEntry) (sandbox.SurveyAction, error) {
			return sandbox.SurveyContinue, nil
		})
	if !os.IsNotExist(err) {
		t.Fatalf("OverlaySurveyWalk error = %v", err)
	}
}

func TestOverlaySurveyReadDirRejectsTraversal(t *testing.T) {
	if _, err := workspace.OverlaySurveyReadDir(t.TempDir(), "../outside", sandbox.SurveyOptions{}); err == nil {
		t.Fatal("OverlaySurveyReadDir accepted parent traversal")
	}
}

func TestWorkerSnapshotMirrorsBuildSymlink(t *testing.T) {
	primary := t.TempDir()
	for _, rel := range []string{"main.go", ".build/arm64-apple-macosx/debug/app.o"} {
		p := filepath.Join(primary, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			testutil.FailErr(t, "mkdir", err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			testutil.FailErr(t, "write", err)
		}
	}
	if err := os.Symlink("arm64-apple-macosx/debug", filepath.Join(primary, ".build", "debug")); err != nil {
		testutil.FailErr(t, "symlink .build/debug", err)
	}

	mgr := workspace.NewManager(t.TempDir(), t.TempDir())
	binding, err := mgr.CreateWorkerWorkspace(context.Background(), primary, "symlink")
	testutil.FailErr(t, "CreateWorkerWorkspace", err)
	defer func() { _ = mgr.DestroyWorkerWorkspace(t.Context(), binding) }()

	link := filepath.Join(binding.Root, ".build", "debug")
	info, err := os.Lstat(link)
	testutil.FailErr(t, "lstat .build/debug", err)
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf(".build/debug must be mirrored as a symlink, got mode %v", info.Mode())
	}
	target, err := os.Readlink(link)
	testutil.FailErr(t, "readlink .build/debug", err)
	if target != "arm64-apple-macosx/debug" {
		t.Fatalf("link target = %q want arm64-apple-macosx/debug", target)
	}
}

func TestWorkerSnapshotPrunesMetadataNotGitignore(t *testing.T) {
	primary := t.TempDir()
	files := map[string]string{
		".gitignore":     "build-out/\n*.log\n",
		"src/app.go":     "package app",
		"target/big.bin": "artifact",
		"node_modules/x": "dep",
		"build-out/o.js": "compiled",
		"debug.log":      "noise",
		"keep/notes.txt": "keep me",
	}
	for rel, body := range files {
		p := filepath.Join(primary, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			testutil.FailErr(t, "mkdir", err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			testutil.FailErr(t, "write", err)
		}
	}

	mgr := workspace.NewManager(t.TempDir(), t.TempDir())
	binding, err := mgr.CreateWorkerWorkspace(context.Background(), primary, "prune")
	testutil.FailErr(t, "CreateWorkerWorkspace", err)
	defer func() { _ = mgr.DestroyWorkerWorkspace(t.Context(), binding) }()

	copied := []string{"src/app.go", "keep/notes.txt", ".gitignore", "build-out/o.js", "debug.log", "target/big.bin", "node_modules/x"}
	for _, rel := range copied {
		if _, err := os.Stat(filepath.Join(binding.Root, filepath.FromSlash(rel))); err != nil {
			testutil.FailErr(t, "expected copied "+rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(binding.Root, ".git")); !os.IsNotExist(err) {
		t.Fatal(".git must not be copied")
	}
}

func TestWorkerSnapshotExcludesLinkedWorktreeAndSubmoduleGitlinks(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git binary not available")
	}

	backingRepo := t.TempDir()
	runGit := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command(gitBin, append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=Test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=Test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		out, runErr := cmd.CombinedOutput()
		if runErr != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, runErr, out)
		}
	}
	runGit(backingRepo, "init")
	testutil.FailErr(t, "write backing file", os.WriteFile(filepath.Join(backingRepo, "tracked.go"), []byte("package main\n"), 0o644))
	runGit(backingRepo, "add", "tracked.go")
	runGit(backingRepo, "commit", "-m", "initial")

	primary := t.TempDir()
	worktreeDir := filepath.Join(primary, "worktree")
	runGit(backingRepo, "worktree", "add", "-b", "wt-branch", worktreeDir)

	submoduleDir := filepath.Join(primary, "submodule")
	testutil.FailErr(t, "mkdir submodule", os.MkdirAll(submoduleDir, 0o755))
	testutil.FailErr(t, "write submodule .git", os.WriteFile(filepath.Join(submoduleDir, ".git"), []byte("gitdir: "+filepath.Join(backingRepo, ".git")+"\n"), 0o644))
	testutil.FailErr(t, "write submodule file", os.WriteFile(filepath.Join(submoduleDir, "sub.go"), []byte("package sub\n"), 0o644))

	// Human listings on the source root still show both .git gitlink files.
	for _, relDir := range []string{"worktree", "submodule"} {
		entries, readErr := sandbox.SurveyReadDir(filepath.Join(primary, relDir), relDir, sandbox.HumanFilesSurveyOptions())
		testutil.FailErr(t, "human SurveyReadDir "+relDir, readErr)
		found := false
		for _, e := range entries {
			if e.Rel == relDir+"/.git" {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("human listing for %s omitted .git: %+v", relDir, entries)
		}
	}

	mgr := workspace.NewManager(t.TempDir(), t.TempDir())
	binding, err := mgr.CreateWorkerWorkspace(context.Background(), primary, "gitlink-isolation")
	testutil.FailErr(t, "CreateWorkerWorkspace", err)
	defer func() { _ = mgr.DestroyWorkerWorkspace(t.Context(), binding) }()

	// Working tree files are copied into the worker branch.
	if _, err := os.Stat(filepath.Join(binding.Root, "worktree", "tracked.go")); err != nil {
		testutil.FailErr(t, "stat copied worktree/tracked.go", err)
	}
	if _, err := os.Stat(filepath.Join(binding.Root, "submodule", "sub.go")); err != nil {
		testutil.FailErr(t, "stat copied submodule/sub.go", err)
	}

	// No gitdir: files exist anywhere in the worker branch.
	testutil.FailErr(t, "walk worker branch", filepath.WalkDir(binding.Root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if strings.HasPrefix(strings.TrimSpace(string(raw)), "gitdir:") {
			t.Fatalf("worker branch retained gitlink file %s", path)
		}
		return nil
	}))

	// Running git status inside the copied worktree fails with "not a git repository".
	cmd := exec.Command(gitBin, "-C", filepath.Join(binding.Root, "worktree"), "status")
	cmd.Env = append(os.Environ(), "GIT_CEILING_DIRECTORIES="+binding.Root)
	out, statusErr := cmd.CombinedOutput()
	if statusErr == nil {
		t.Fatalf("expected git status in worker branch worktree to fail, got:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(string(out)), "not a git repository") {
		t.Fatalf("expected 'not a git repository' error, got: %s", out)
	}

	// OverlaySurveyReadDir also skips gitlink .git files if one is present on disk.
	testutil.FailErr(t, "plant branch gitlink", os.WriteFile(filepath.Join(binding.Root, "worktree", ".git"), []byte("gitdir: "+filepath.Join(backingRepo, ".git")+"\n"), 0o644))
	overlayEntries, err := workspace.OverlaySurveyReadDir(binding.Root, "worktree", sandbox.SurveyOptions{IncludeHidden: true})
	testutil.FailErr(t, "OverlaySurveyReadDir worktree", err)
	for _, e := range overlayEntries {
		if e.Rel == "worktree/.git" {
			t.Fatalf("OverlaySurveyReadDir must omit gitlink .git file: %+v", overlayEntries)
		}
	}
}
