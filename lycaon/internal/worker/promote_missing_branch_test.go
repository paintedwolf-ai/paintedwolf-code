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

// Missing branches cannot erase unchanged primary content.
func TestAssessPromotePaths3WayMissingBranchDoesNotZeroPrimary(t *testing.T) {
	primary := t.TempDir()
	branchRoot := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-missing")
	baseBody := "package main\n\nfunc main() {}\n"
	testutil.FailErr(t, "WriteFile primary", os.WriteFile(filepath.Join(primary, "main.go"), []byte(baseBody), 0o644))

	baseline := map[string]testbaseline.File{
		"main.go": {Content: baseBody},
	}
	raw := testbaseline.FromFiles(t, baseline)

	task := &api.WorkerTask{
		ID:                    "job-missing",
		ParentSessionID:       "sess-1",
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         primary,
		WorkspaceRoot:         branchRoot,
		MergeStatus:           api.WorkerMergeStatusPending,
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"main.go"}},
		WorkspaceBaselinePath: raw,
		Status:                api.WorkerStatusComplete,
	}

	got, err := worker.AssessPromotePaths3Way(context.Background(), nil, "sess-1", task, []string{"main.go"})
	testutil.FailErr(t, "AssessPromotePaths3Way", err)
	if len(got.Conflicts) != 0 {
		t.Fatalf("conflicts=%v want none (noop)", got.Conflicts)
	}
	if len(got.CleanPaths) != 1 || got.CleanPaths[0] != "main.go" {
		t.Fatalf("clean=%v", got.CleanPaths)
	}
	if len(got.MergeResults) != 0 {
		t.Fatalf("must not write merge content for missing branch, got %+v", got.MergeResults)
	}

	after, err := os.ReadFile(filepath.Join(primary, "main.go"))
	testutil.FailErr(t, "ReadFile primary", err)
	if string(after) != baseBody {
		t.Fatalf("primary mutated=%q want unchanged", after)
	}
}

func TestAssessPromotePaths3WayMissingBranchConflictsWhenPrimaryMoved(t *testing.T) {
	primary := t.TempDir()
	branchRoot := filepath.Join(primary, settingsoverlay.DirName(), "overlays", "job-missing")
	baseBody := "base\n"
	primaryBody := "primary moved\n"
	testutil.FailErr(t, "WriteFile primary", os.WriteFile(filepath.Join(primary, "a.go"), []byte(primaryBody), 0o644))

	baseline := map[string]testbaseline.File{
		"a.go": {Content: baseBody},
	}
	raw := testbaseline.FromFiles(t, baseline)

	task := &api.WorkerTask{
		ProjectID:             testdbseed.DefaultProjectID,
		WorkspacePath:         primary,
		WorkspaceRoot:         branchRoot,
		Scope:                 &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"a.go"}},
		WorkspaceBaselinePath: raw,
	}

	got, err := worker.AssessPromotePaths3Way(context.Background(), nil, "sess", task, []string{"a.go"})
	testutil.FailErr(t, "AssessPromotePaths3Way", err)
	if len(got.Conflicts) != 1 {
		t.Fatalf("conflicts=%v want one", got.Conflicts)
	}
	if got.Conflicts[0].Reason != api.WorkerPromoteReasonBranchMissing {
		t.Fatalf("reason=%q want %q", got.Conflicts[0].Reason, api.WorkerPromoteReasonBranchMissing)
	}
}
