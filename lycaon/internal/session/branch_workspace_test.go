package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/session/workerworkspace"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/internal/workspace"
)

func TestBranchWorkspaceRejectsMissingMeta(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "project")
	testutil.FailErr(t, "mkdir project", os.MkdirAll(dir, 0o755))
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "only.txt"), []byte("x"), 0o644))
	roots := []projectroot.RootRef{{ID: "p", Label: "root", Path: dir, IsPrimary: true}}
	mgr := workspace.NewManager(filepath.Join(base, "branches"), filepath.Join(base, "seeds"))
	binding, _, err := mgr.CreateWorkerWorkspaceFromSources(context.Background(), roots, roots, "p", "job-meta")
	testutil.FailErr(t, "CreateWorkerWorkspaceFromSources", err)
	testutil.FailErr(t, "remove meta", os.RemoveAll(enginepaths.MetaDirForBranchRoot(binding.Root)))

	branch, err := newBranchWorkspace(binding.Root)
	testutil.FailErr(t, "newBranchWorkspace", err)
	if err := branch.ValidateMeta(context.Background()); !os.IsNotExist(err) {
		t.Fatalf("ValidateMeta error = %v, want missing metadata", err)
	}
}

func TestBranchWorkspaceRejectsInvalidMultiRootPath(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "primary")
	secondary := filepath.Join(base, "secondary")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(primary, 0o755))
	testutil.FailErr(t, "mkdir secondary", os.MkdirAll(secondary, 0o755))
	roots := []projectroot.RootRef{
		{ID: "primary", Label: "app", Path: primary, IsPrimary: true},
		{ID: "secondary", Label: "docs", Path: secondary},
	}
	mgr := workspace.NewManager(filepath.Join(base, "branches"), filepath.Join(base, "seeds"))
	binding, _, err := mgr.CreateWorkerWorkspaceFromSources(context.Background(), roots, roots, "primary", "job-roots")
	testutil.FailErr(t, "create branch", err)
	branch, err := newBranchWorkspace(binding.Root)
	testutil.FailErr(t, "new branch workspace", err)

	if err := branch.EnsureParents(context.Background(), "../outside/file.txt"); err == nil {
		t.Fatal("EnsureParents accepted parent traversal")
	}
	if err := branch.EnsureParents(context.Background(), "unknown/file.txt"); err == nil {
		t.Fatal("EnsureParents accepted an unknown root")
	}
}

func TestEnsureWorkerBranchRestoresPrimaryReadDeniesFromMetadata(t *testing.T) {
	base := t.TempDir()
	primary := filepath.Join(base, "primary")
	testutil.FailErr(t, "mkdir primary", os.MkdirAll(primary, 0o755))
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(primary, "module.py"), []byte("value = 1\n"), 0o644))
	roots := []projectroot.RootRef{{ID: "primary", Label: "app", Path: primary, IsPrimary: true}}
	workspaceManager := workspace.NewManager(filepath.Join(base, "branches"), filepath.Join(base, "seeds"))
	binding, _, err := workspaceManager.CreateWorkerWorkspaceFromSources(
		context.Background(), roots, roots, "primary", "job-read-deny",
	)
	testutil.FailErr(t, "create branch", err)
	branch, err := newBranchWorkspace(binding.Root)
	testutil.FailErr(t, "new branch workspace", err)

	tctx, err := workerworkspace.New(nil, nil, newBranchWorkspace).EnsureBranch(context.Background(), tools.ToolContext{
		WorkerBranchRoot: binding.Root, BranchWorkspace: branch,
	})
	testutil.FailErr(t, "ensure existing branch", err)
	if len(tctx.WorkerSourceRoots) != 1 || tctx.WorkerSourceRoots[0] != primary {
		t.Fatalf("worker source roots = %v want [%s]", tctx.WorkerSourceRoots, primary)
	}
}
