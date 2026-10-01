package workspace_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/enginepaths"
	"os"
	"path/filepath"
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
