package worker_test

import (
	"context"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func loadTasks(tasks []api.WorkerTask) func(context.Context) ([]api.WorkerTask, error) {
	return func(context.Context) ([]api.WorkerTask, error) { return tasks, nil }
}

func TestReconcileProjectSandboxesRetainsActiveWriteSandboxes(t *testing.T) {
	sandboxRoot := t.TempDir()
	primary := t.TempDir()
	live := enginepaths.JobBranchDir(sandboxRoot, primary, "live-run")
	merged := enginepaths.JobBranchDir(sandboxRoot, primary, "merged")
	for _, dir := range []string{live, merged} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			testutil.FailErr(t, "mkdir sandbox", err)
		}
	}

	tasks := []api.WorkerTask{
		{ID: "live-run", Status: api.WorkerStatusRunning, WorkspaceRoot: live, Scope: writeScope()},
		{ID: "merged", Status: api.WorkerStatusComplete, WorkspaceRoot: merged, Scope: writeScope(), MergeStatus: api.WorkerMergeStatusMerged},
	}

	removed, err := worker.ReconcileProjectSandboxes(context.Background(), sandboxRoot, primary, loadTasks(tasks))
	testutil.FailErr(t, "ReconcileProjectSandboxes", err)
	if removed != 1 {
		t.Fatalf("removed = %d want 1 terminal sandbox", removed)
	}
	if _, err := os.Stat(live); err != nil {
		testutil.FailErr(t, "live sandbox", err)
	}
	if _, err := os.Stat(merged); !os.IsNotExist(err) {
		t.Fatal("merged sandbox should be removed")
	}
}

func TestReconcileProjectSandboxesRetainsWorkersFromOtherProjectID(t *testing.T) {
	sandboxRoot := t.TempDir()
	primary := t.TempDir()
	live := enginepaths.JobBranchDir(sandboxRoot, primary, "live-run")
	if err := os.MkdirAll(live, 0o755); err != nil {
		testutil.FailErr(t, "mkdir sandbox", err)
	}

	tasks := []api.WorkerTask{{
		ID:            "live-run",
		ProjectID:     "other-project-id",
		Status:        api.WorkerStatusRunning,
		WorkspacePath: primary,
		WorkspaceRoot: live,
		Scope:         writeScope(),
	}}

	removed, err := worker.ReconcileProjectSandboxes(context.Background(), sandboxRoot, primary, loadTasks(tasks))
	testutil.FailErr(t, "ReconcileProjectSandboxes", err)
	if removed != 0 {
		t.Fatalf("removed = %d want 0 when active write worker retains sandbox", removed)
	}
	if _, err := os.Stat(live); err != nil {
		testutil.FailErr(t, "live sandbox", err)
	}
}
