package worker_test

import (
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTaskRootRefsUsesBranchSnapshotAfterProjectRename(t *testing.T) {
	ctx := t.Context()
	reg := project.NewMemoryRegistry()
	p, err := reg.Create(ctx, project.CreateParams{Roots: []project.AttachRootParams{
		{Path: t.TempDir(), Label: "app"}, {Path: t.TempDir(), Label: "docs"},
	}})
	testutil.FailErr(t, "create project", err)
	roots := project.RootRefsFrom(p)
	mgr := workspace.NewManager(filepath.Join(t.TempDir(), "branches"), filepath.Join(t.TempDir(), "seeds"))
	binding, _, err := mgr.CreateWorkerWorkspaceFromSources(ctx, roots, roots, roots[0].ID, "job-1")
	testutil.FailErr(t, "create workspace", err)
	renamed := "manuals"
	_, err = reg.PatchRoot(ctx, p.ID, roots[1].ID, project.PatchRootParams{Label: &renamed})
	testutil.FailErr(t, "rename root", err)

	refs := worker.TaskRootRefs(ctx, &api.WorkerTask{ProjectID: p.ID, WorkspaceRoot: binding.Root}, reg)
	if len(refs) != 2 || refs[1].Label != "docs" || refs[1].ID != roots[1].ID {
		t.Fatalf("refs = %+v", refs)
	}
}
