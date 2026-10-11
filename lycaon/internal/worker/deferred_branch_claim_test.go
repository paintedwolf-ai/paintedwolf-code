package worker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/sourceledger"
	"github.com/lycaon/lycaon/internal/testbaseline"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workspace"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestClaimNextDoesNotProvisionBranch(t *testing.T) {
	dir := t.TempDir()
	testutil.FailErr(t, "write", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644))
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertProjectRootWithID(t, sqlDB, testdbseed.DefaultProjectID, "root-id", dir)

	q := NewSQLQueue(sqlDB, 4)
	q.SetBaselineStore(sourceledger.New(sqlDB, filepath.Join(testbaseline.DataDir(t, sqlDB), "source-content")).Baselines)
	q.SetWorkerWorkspaceManager(workspace.NewManager(filepath.Join(testbaseline.DataDir(t, sqlDB), "worker-branches"), t.TempDir()))

	id, err := q.Enqueue(context.Background(), api.WorkerTask{
		ProjectID:       testdbseed.DefaultProjectID,
		WorkspacePath:   dir,
		WorkspaceRootID: "root-id",
		Scope:           &api.TaskScope{Mode: api.TaskScopeModeWrite, Paths: []string{"main.go"}},
		AgentType:       "implementer",
		Prompt:          "Update main.go",
		Brief:           "Update main.go",
	})
	testutil.FailErr(t, "Enqueue", err)

	task, err := q.ClaimNext(context.Background(), ClaimRequest{
		ProjectID:       testdbseed.DefaultProjectID,
		ClaimedBy:       "test",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "ClaimNext", err)
	if task.ID != id {
		t.Fatalf("id=%q want %q", task.ID, id)
	}
	if strings.TrimSpace(task.WorkspaceRoot) != "" {
		t.Fatalf("WorkspaceRoot=%q want empty at ClaimNext", task.WorkspaceRoot)
	}
	if strings.TrimSpace(task.WorkspaceBaselinePath) != "" {
		t.Fatalf("baseline=%q want empty until branch materialization", task.WorkspaceBaselinePath)
	}
	testutil.FailErr(t, "update canonical before branch claim", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package claimed\n"), 0o644))

	claimed, err := q.ClaimWorkerBranch(context.Background(), id)
	testutil.FailErr(t, "ClaimWorkerBranch", err)
	if strings.TrimSpace(claimed.WorkspaceRoot) == "" {
		t.Fatal("ClaimWorkerBranch must set WorkspaceRoot")
	}
	if strings.TrimSpace(claimed.WorkspaceBaselinePath) == "" {
		t.Fatal("ClaimWorkerBranch must bind the branch baseline")
	}
	if _, err := os.Stat(claimed.WorkspaceRoot); err != nil {
		testutil.FailErr(t, "branch dir", err)
	}
	branchBytes, err := os.ReadFile(filepath.Join(claimed.WorkspaceRoot, "main.go"))
	testutil.FailErr(t, "read branch snapshot", err)
	if string(branchBytes) != "package claimed\n" {
		t.Fatalf("branch bytes = %q", branchBytes)
	}
	testutil.FailErr(t, "update canonical after branch claim", os.WriteFile(filepath.Join(dir, "main.go"), []byte("package later\n"), 0o644))
	branchBytes, err = os.ReadFile(filepath.Join(claimed.WorkspaceRoot, "main.go"))
	testutil.FailErr(t, "read immutable branch snapshot", err)
	if string(branchBytes) != "package claimed\n" {
		t.Fatalf("branch drifted with canonical source: %q", branchBytes)
	}
	again, err := q.ClaimWorkerBranch(context.Background(), id)
	testutil.FailErr(t, "ClaimWorkerBranch idempotent", err)
	if again.WorkspaceRoot != claimed.WorkspaceRoot {
		t.Fatalf("idempotent root=%q want %q", again.WorkspaceRoot, claimed.WorkspaceRoot)
	}
}
