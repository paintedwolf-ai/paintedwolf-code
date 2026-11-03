package worker_test

import (
	"context"
	"os"
	"testing"

	"github.com/lycaon/lycaon/internal/enginepaths"
	"github.com/lycaon/lycaon/internal/layeredfs/adapter"
	"github.com/lycaon/lycaon/internal/layeredfs/checkpoint"
	"github.com/lycaon/lycaon/internal/layeredfs/recovery"
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

func TestReconcileProjectWorkerViewsPreservesUnpromotedOverlayAndSettlesTerminalViews(t *testing.T) {
	sandboxRoot := t.TempDir()
	primary := t.TempDir()
	unpromoted := enginepaths.JobBranchDir(sandboxRoot, primary, "failed-with-overlay")
	merged := enginepaths.JobBranchDir(sandboxRoot, primary, "merged-job")
	for _, dir := range []string{unpromoted, merged} {
		testutil.FailErr(t, "mkdir sandbox", os.MkdirAll(dir, 0o755))
	}

	cpStore, err := checkpoint.NewStore(t.TempDir())
	testutil.FailErr(t, "NewStore", err)
	coord, err := recovery.NewCoordinator(recovery.CoordinatorConfig{
		RootDir:         t.TempDir(),
		CheckpointStore: cpStore,
	})
	testutil.FailErr(t, "NewCoordinator", err)

	testutil.FailErr(t, "SaveWorkerJournal", coord.SaveWorkerJournal(recovery.WorkerLifecycleJournal{
		FormatVersion: recovery.FormatVersion,
		WorkerID:      "merged-job",
		ViewID:        "view-merged",
		TaskID:        "t-1",
		ProjectID:     "p-1",
		AdapterID:     adapter.AdapterFUSELinux,
	}))
	testutil.FailErr(t, "RecordMountIdentity", coord.RecordMountIdentity(recovery.MountIdentityRecord{
		ViewID:         "view-merged",
		WorkerID:       "merged-job",
		AdapterID:      adapter.AdapterFUSELinux,
		RuntimeViewDir: t.TempDir(),
		BackingDir:     t.TempDir(),
	}))

	tasks := []api.WorkerTask{
		{
			ID:                   "failed-with-overlay",
			Status:               api.WorkerStatusFailed,
			MergeStatus:          api.WorkerMergeStatusPending,
			WorkspaceOverlayPath: "/tmp/overlay-record",
			WorkspaceRoot:        unpromoted,
			Scope:                writeScope(),
		},
		{
			ID:            "merged-job",
			Status:        api.WorkerStatusComplete,
			MergeStatus:   api.WorkerMergeStatusMerged,
			WorkspaceRoot: merged,
			Scope:         writeScope(),
		},
	}

	removed, err := worker.ReconcileProjectWorkerViews(t.Context(), sandboxRoot, primary, coord, loadTasks(tasks))
	testutil.FailErr(t, "ReconcileProjectWorkerViews", err)
	if removed != 2 {
		t.Fatalf("removed = %d want 2 (1 merged sandbox + 1 settled view)", removed)
	}
	if _, err := os.Stat(unpromoted); err != nil {
		t.Fatalf("expected unpromoted overlay sandbox retained: %v", err)
	}
}
