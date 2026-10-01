package worker

import (
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkflowWorkIdentitySurvivesQueuePersistence(t *testing.T) {
	store := obligationTestStore(t)
	task := api.WorkerTask{ID: "job", ProjectID: testdbseed.DefaultProjectID, WorkflowRunID: "run-1", WorkflowPhase: "execute", WorkflowWorkID: "leg-1", AgentType: "security-reviewer", Prompt: "Review", Brief: "Review", Status: api.WorkerStatusPending, ExecutionTarget: api.ExecutionTargetLocal}
	testutil.FailErr(t, "insert bound task", store.InsertTask(t.Context(), task))
	rows, err := store.ListByWorkflowRunID(t.Context(), "run-1")
	testutil.FailErr(t, "read bound task", err)
	if len(rows) != 1 || rows[0].WorkflowPhase != "execute" || rows[0].WorkflowWorkID != "leg-1" {
		t.Fatalf("tasks=%#v", rows)
	}
}
