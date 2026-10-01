package worker_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/settingsoverlay"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func promoteTask(t *testing.T, primary, branch string, baseline map[string]testbaseline.File, paths []string) *api.WorkerTask {
	t.Helper()
	raw := testbaseline.FromFiles(t, baseline)
	return &api.WorkerTask{
		ID:                    "job-1",
		ParentSessionID:       "sess-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         primary,
		WorkspaceRoot:         branch,
		MergeStatus:           api.WorkerMergeStatusPending,
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: paths},
		WorkspaceBaselinePath: raw,
		Status:                api.WorkerStatusComplete,
	}
}

func TestAssessPromotePaths3WayCleanWhenOnlyBranchChanged(t *testing.T) {
	primary := t.TempDir()
	branch := t.TempDir()
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(primary, "a.go"), []byte("v1\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "a.go"), []byte("v2\n"), 0o644))

	baseline := map[string]testbaseline.File{
		"a.go": {Content: "v1\n"},
	}
	task := promoteTask(t, primary, branch, baseline, []string{"a.go"})
	got, err := worker.AssessPromotePaths3Way(context.Background(), nil, "sess-1", task, []string{"a.go"})
	testutil.FailErr(t, "AssessPromotePaths3Way", err)
	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts=%v want none", got.Conflicts)
	}
	if len(got.CleanPaths) != 1 || got.CleanPaths[0] != "a.go" {
		t.Fatalf("clean=%v", got.CleanPaths)
	}
	if len(got.MergeResults) != 1 || got.MergeResults[0].Content != "v2\n" {
		t.Fatalf("merge=%+v", got.MergeResults)
	}
}

func TestAssessPromotePaths3WayConflictWhenBothChanged(t *testing.T) {
	primary := t.TempDir()
	branch := t.TempDir()
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(primary, "a.go"), []byte("primary-changed\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "a.go"), []byte("branch-edit\n"), 0o644))

	baseline := map[string]testbaseline.File{
		"a.go": {Content: "base\n"},
	}
	task := promoteTask(t, primary, branch, baseline, []string{"a.go"})
	got, err := worker.AssessPromotePaths3Way(context.Background(), nil, "sess-1", task, []string{"a.go"})
	testutil.FailErr(t, "AssessPromotePaths3Way", err)
	if len(got.Conflicts) != 1 {
		t.Fatalf("conflicts=%v want one", got.Conflicts)
	}
	if len(got.Conflicts[0].Hunks) == 0 {
		t.Fatalf("expected hunks on conflict: %+v", got.Conflicts[0])
	}
}

func TestAssessPromotePaths3WayNoOpFoldsToCleanPaths(t *testing.T) {
	// Equal branch and primary content is a clean no-op.
	primary := t.TempDir()
	branch := t.TempDir()
	body := []byte("same\n")
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(primary, "a.go"), body, 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "a.go"), body, 0o644))

	baseline := map[string]testbaseline.File{
		"a.go": {Content: "old\n"},
	}
	task := promoteTask(t, primary, branch, baseline, []string{"a.go"})
	got, err := worker.AssessPromotePaths3Way(context.Background(), nil, "sess-1", task, []string{"a.go"})
	testutil.FailErr(t, "AssessPromotePaths3Way", err)
	if len(got.CleanPaths) != 1 || got.CleanPaths[0] != "a.go" {
		t.Fatalf("clean=%v, want [a.go]", got.CleanPaths)
	}
	if len(got.Conflicts) != 0 {
		t.Fatalf("expected no conflicts, got %v", got.Conflicts)
	}
}

func TestAssessPromotePaths3WayEmptyBaselineRunsThreeWayMerge(t *testing.T) {
	primary := t.TempDir()
	branch := t.TempDir()
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(primary, "a.go"), []byte("p\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "a.go"), []byte("b\n"), 0o644))

	task := promoteTask(t, primary, branch, nil, []string{"a.go"})
	got, err := worker.AssessPromotePaths3Way(context.Background(), nil, "sess-1", task, []string{"a.go"})
	testutil.FailErr(t, "AssessPromotePaths3Way", err)
	if len(got.Conflicts) != 1 {
		t.Fatalf("conflicts=%v want one three-way conflict", got.Conflicts)
	}
	if len(got.Conflicts[0].Hunks) == 0 {
		t.Fatalf("expected hunks on three-way conflict: %+v", got.Conflicts[0])
	}
}

func TestAssessPromotePaths3WayRejectsMissingBaseline(t *testing.T) {
	primary := t.TempDir()
	branch := t.TempDir()
	task := promoteTask(t, primary, branch, nil, []string{"a.go"})
	task.WorkspaceBaselinePath = ""

	_, err := worker.AssessPromotePaths3Way(context.Background(), nil, "sess-1", task, []string{"a.go"})
	if err == nil || !strings.Contains(err.Error(), "valid workspace baseline") {
		t.Fatalf("AssessPromotePaths3Way error = %v", err)
	}
}

func TestAssessPromotePaths3WayNewPathChangedOnBothSidesConflicts(t *testing.T) {
	primary := t.TempDir()
	branch := t.TempDir()
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(primary, "drift.go"), []byte("p\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "drift.go"), []byte("b\n"), 0o644))

	baseline := map[string]testbaseline.File{
		"baseline.go": {Content: "base\n"},
	}
	task := promoteTask(t, primary, branch, baseline, []string{"baseline.go"})
	got, err := worker.AssessPromotePaths3Way(context.Background(), nil, "sess-1", task, []string{"drift.go"})
	testutil.FailErr(t, "AssessPromotePaths3Way", err)
	if len(got.Conflicts) != 1 || got.Conflicts[0].Reason != api.WorkerPromoteReasonThreeWayUnresolved {
		t.Fatalf("conflicts=%+v", got.Conflicts)
	}
	if got.Conflicts[0].Base != "" || len(got.Conflicts[0].Hunks) == 0 {
		t.Fatalf("new path must merge from an empty base: %+v", got.Conflicts[0])
	}
	if got.Conflicts[0].Primary != "p\n" || got.Conflicts[0].Branch != "b\n" {
		t.Fatalf("conflict must include merge bodies: %+v", got.Conflicts[0])
	}
}

func TestAssessPromotePaths3WayNewBranchFileIsClean(t *testing.T) {
	primary := t.TempDir()
	branch := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-tests")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(filepath.Join(branch, "tests"), 0o755))

	body := "import pytest\n\ndef test_ok():\n    assert True\n"
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branch, "tests", "test_core_utils.py"), []byte(body), 0o644))

	task := promoteTask(t, primary, branch, nil, []string{"unrelated.go"})
	got, err := worker.AssessPromotePaths3Way(context.Background(), nil, "sess-1", task, []string{"tests/test_core_utils.py"})
	testutil.FailErr(t, "AssessPromotePaths3Way", err)
	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts=%v want none for independent new file", got.Conflicts)
	}
	if len(got.CleanPaths) != 1 || got.CleanPaths[0] != "tests/test_core_utils.py" {
		t.Fatalf("clean=%v", got.CleanPaths)
	}
	if len(got.MergeResults) != 1 || got.MergeResults[0].Content != body {
		t.Fatalf("merge=%+v", got.MergeResults)
	}
}

func TestAssessPromotePaths3WayNewFileAddedOnBothSidesConflicts(t *testing.T) {
	primary := t.TempDir()
	branch := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-b")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(filepath.Join(primary, "tests"), 0o755))
	testutil.FailErr(t, "MkdirAll branch", os.MkdirAll(filepath.Join(branch, "tests"), 0o755))

	siblingBody := "def test_from_a():\n    pass\n"
	branchBody := "def test_from_b():\n    pass\n"
	testutil.FailErr(t, "WriteFile primary", os.WriteFile(filepath.Join(primary, "tests", "test_shared.py"), []byte(siblingBody), 0o644))
	testutil.FailErr(t, "WriteFile branch", os.WriteFile(filepath.Join(branch, "tests", "test_shared.py"), []byte(branchBody), 0o644))

	task := promoteTask(t, primary, branch, nil, []string{"unrelated.go"})
	got, err := worker.AssessPromotePaths3Way(context.Background(), nil, "sess-1", task, []string{"tests/test_shared.py"})
	testutil.FailErr(t, "AssessPromotePaths3Way", err)
	if len(got.Conflicts) != 1 {
		t.Fatalf("conflicts=%v want one concurrent create conflict", got.Conflicts)
	}
	if len(got.Conflicts[0].Hunks) == 0 {
		t.Fatalf("expected hunks: %+v", got.Conflicts[0])
	}
	if got.Conflicts[0].Primary != siblingBody || got.Conflicts[0].Branch != branchBody {
		t.Fatalf("conflict bodies=%+v", got.Conflicts[0])
	}
}

type overlapSessionLister struct {
	tasks []api.WorkerTask
}

func (s overlapSessionLister) ListPendingOverlayPromote(context.Context, string) ([]api.WorkerTask, error) {
	return append([]api.WorkerTask(nil), s.tasks...), nil
}

func (s overlapSessionLister) ListLiveOverlaysForSession(context.Context, string) ([]api.WorkerTask, error) {
	return s.ListPendingOverlayPromote(context.Background(), "")
}

func TestAssessPromotePaths3WaySiblingOverlapOnCleanPath(t *testing.T) {
	primary := t.TempDir()
	branchA := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-a")
	branchB := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-b")
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branchA, 0o755))
	testutil.FailErr(t, "MkdirAll", os.MkdirAll(branchB, 0o755))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(primary, "shared.go"), []byte("v1\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branchA, "shared.go"), []byte("v2\n"), 0o644))
	testutil.FailErr(t, "WriteFile", os.WriteFile(filepath.Join(branchB, "shared.go"), []byte("v3\n"), 0o644))

	baseline := map[string]testbaseline.File{
		"shared.go": {Content: "v1\n"},
	}
	taskA := promoteTask(t, primary, branchA, baseline, []string{"shared.go"})
	taskA.ID = "job-a"
	sibling := promoteTask(t, primary, branchB, baseline, []string{"shared.go"})
	sibling.ID = "job-b"

	svc := &worker.MergeService{
		Sessions: overlapSessionLister{tasks: []api.WorkerTask{*taskA, *sibling}},
	}
	got, err := worker.AssessPromotePaths3Way(context.Background(), svc, "sess-1", taskA, []string{"shared.go"})
	testutil.FailErr(t, "AssessPromotePaths3Way", err)
	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts=%v want none", got.Conflicts)
	}
	if len(got.OverlapJobIDs) != 1 || got.OverlapJobIDs[0] != "job-b" {
		t.Fatalf("overlap_job_ids=%v want [job-b]", got.OverlapJobIDs)
	}
}
