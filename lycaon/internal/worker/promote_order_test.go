package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAssessPromotePaths3WayCleanIfFirstWhenSiblingWouldConflict(t *testing.T) {
	primary := t.TempDir()
	branchA := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-a")
	branchB := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-b")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branchA, 0o755))
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branchB, 0o755))

	body := "a\nb\nc\n"
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(primary, "shared.go"), []byte(body), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branchA, "shared.go"), []byte("a\nA\nc\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branchB, "shared.go"), []byte("a\nB\nc\n"), 0o644))

	baseline := map[string]testbaseline.File{
		"shared.go": {Content: body},
	}
	raw := testbaseline.FromFiles(t, baseline)

	taskA := &api.WorkerTask{
		ID:                    "job-a",
		ParentSessionID:       "sess-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         primary,
		WorkspaceRoot:         branchA,
		MergeStatus:           api.WorkerMergeStatusPending,
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"shared.go"}},
		WorkspaceBaselinePath: raw,
		Status:                api.WorkerStatusComplete,
	}
	taskB := *taskA
	taskB.ID = "job-b"
	taskB.WorkspaceRoot = branchB

	svc := &worker.MergeService{
		Sessions: overlapSessionLister{tasks: []api.WorkerTask{*taskA, taskB}},
	}
	got, err := worker.AssessPromotePaths3Way(context.Background(), svc, "sess-1", taskA, []string{"shared.go"})
	testutil.FailErr(t, "AssessPromotePaths3Way", err)
	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts=%v want none", got.Conflicts)
	}
	var order worker.PromotePathOrder
	for _, row := range got.PathOrders {
		if row.Path == "shared.go" {
			order = row
			break
		}
	}
	if order.Order != api.WorkerPromoteOrderCleanIfFirst {
		t.Fatalf("order=%q want clean_if_first", order.Order)
	}
	if len(order.BlockedBy) != 1 || order.BlockedBy[0] != "job-b" {
		t.Fatalf("blocked_by=%v want [job-b]", order.BlockedBy)
	}
}

func TestAssessPromotePaths3WayIndependentWhenLineDistant(t *testing.T) {
	primary := t.TempDir()
	branchA := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-a")
	branchB := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-b")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branchA, 0o755))
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branchB, 0o755))

	body := "a\nb\nc\n"
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(primary, "shared.go"), []byte(body), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branchA, "shared.go"), []byte("a\nB\nc\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branchB, "shared.go"), []byte("a\nb\nC\n"), 0o644))

	baseline := map[string]testbaseline.File{
		"shared.go": {Content: body},
	}
	raw := testbaseline.FromFiles(t, baseline)

	taskA := &api.WorkerTask{
		ID:                    "job-a",
		ParentSessionID:       "sess-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         primary,
		WorkspaceRoot:         branchA,
		MergeStatus:           api.WorkerMergeStatusPending,
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"shared.go"}},
		WorkspaceBaselinePath: raw,
		Status:                api.WorkerStatusComplete,
	}
	taskB := *taskA
	taskB.ID = "job-b"
	taskB.WorkspaceRoot = branchB

	svc := &worker.MergeService{
		Sessions: overlapSessionLister{tasks: []api.WorkerTask{*taskA, taskB}},
	}
	got, err := worker.AssessPromotePaths3Way(context.Background(), svc, "sess-1", taskA, []string{"shared.go"})
	testutil.FailErr(t, "AssessPromotePaths3Way", err)
	for _, row := range got.PathOrders {
		if row.Path == "shared.go" && row.Order == api.WorkerPromoteOrderCleanIfFirst {
			t.Fatalf("line-distant edits should not be clean_if_first: %+v", row)
		}
	}
}
