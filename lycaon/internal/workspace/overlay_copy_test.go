package workspace_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
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
	defer func() { _ = mgr.DestroyWorkerWorkspace(binding) }()

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
	defer func() { _ = mgr.DestroyWorkerWorkspace(binding) }()

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
	defer func() { _ = mgr.DestroyWorkerWorkspace(binding) }()

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
	defer func() { _ = mgr.DestroyWorkerWorkspace(binding) }()

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

func TestMaterializeBoundedWorkspaceGitIsolationAndLimitEnforcement(t *testing.T) {
	ctx := context.Background()
	primary := t.TempDir()
	testutil.FailErr(t, "mkdir .git", os.MkdirAll(filepath.Join(primary, ".git", "objects", "ab"), 0o755))
	testutil.FailErr(t, "mkdir submodule gitdir", os.MkdirAll(filepath.Join(primary, ".git", "modules", "sub"), 0o755))
	testutil.FailErr(t, "write HEAD", os.WriteFile(filepath.Join(primary, ".git", "HEAD"), []byte("ref: refs/heads/main\n"), 0o644))
	testutil.FailErr(t, "write sub HEAD", os.WriteFile(filepath.Join(primary, ".git", "modules", "sub", "HEAD"), []byte("ref: refs/heads/sub\n"), 0o644))
	testutil.FailErr(t, "mkdir sub", os.MkdirAll(filepath.Join(primary, "sub"), 0o755))
	testutil.FailErr(t, "write gitlink", os.WriteFile(filepath.Join(primary, "sub", ".git"), []byte("gitdir: ../.git/modules/sub\n"), 0o644))
	testutil.FailErr(t, "write code", os.WriteFile(filepath.Join(primary, "sub", "code.go"), []byte("package sub\n"), 0o644))

	runtimeDir := t.TempDir()
	grants := []adapter.RootGrant{{RootID: "primary", Label: "primary", SourcePath: primary, IsPrimary: true}}

	view, err := workspace.MaterializeBoundedWorkspace(
		ctx, "mat-view-ok", runtimeDir, grants, adapter.WorkspaceSelection{}, nil, 0, 0, adapter.ReasonMountDenied,
	)
	testutil.FailErr(t, "MaterializeBoundedWorkspace", err)
	defer func() { _ = view.Core.ReleaseView() }()

	if _, err := os.Lstat(filepath.Join(view.PrimaryWorkPath, ".git")); !os.IsNotExist(err) {
		t.Fatalf("raw .git directory must not exist in materialized workspace: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(view.PrimaryWorkPath, "sub", ".git")); !os.IsNotExist(err) {
		t.Fatalf("raw gitlink file must not exist in materialized workspace: %v", err)
	}
	base, ok := view.Core.CapturedBase(layeredfs.QualifiedPath{RootID: "primary", Rel: "sub/code.go"})
	if !ok || base.SHA256 == "" {
		t.Fatalf("expected pre-captured seed base in Core, got ok=%v base=%+v", ok, base)
	}

	// Enforce actual entry limit and clean up incomplete seed without touching sibling view.
	_, limErr := workspace.MaterializeBoundedWorkspace(
		ctx, "mat-view-limit", runtimeDir, grants, adapter.WorkspaceSelection{}, nil, 2, 0, adapter.ReasonMountDenied,
	)
	var prepLim *adapter.PreparationLimitError
	if !errors.As(limErr, &prepLim) || prepLim.ReasonCode != adapter.ReasonWorkspacePreparationLimit || !prepLim.CleanedIncompleteSeed {
		t.Fatalf("expected PreparationLimitError with workspace_preparation_limit, got %v", limErr)
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "views", "mat-view-limit")); !os.IsNotExist(err) {
		t.Fatalf("incomplete limited view was not cleaned up: %v", err)
	}
	if _, err := os.Stat(filepath.Join(view.PrimaryWorkPath, "sub", "code.go")); err != nil {
		t.Fatalf("sibling materialized view was disrupted: %v", err)
	}

	// Verify real Manager.CreateWorkerWorkspace enforces entry/byte limits and cleans partial branch.
	branchBase := t.TempDir()
	mgr := workspace.NewManager(branchBase, t.TempDir()).WithPreparationLimits(1, 4)
	if _, mgrErr := mgr.CreateWorkerWorkspace(ctx, primary, "mgr-limit-fail"); !errors.As(mgrErr, &prepLim) || prepLim.ReasonCode != adapter.ReasonWorkspacePreparationLimit || !prepLim.CleanedIncompleteSeed {
		t.Fatalf("expected Manager.CreateWorkerWorkspace to fail with workspace_preparation_limit, got %v", mgrErr)
	}
}

