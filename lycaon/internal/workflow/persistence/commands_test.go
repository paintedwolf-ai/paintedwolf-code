package persistence_test

import (
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowpersistence "github.com/lycaon/lycaon/internal/workflow/persistence"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

type countingRunnableNotifier struct{ calls int }

func (n *countingRunnableNotifier) NotifyRunnable() { n.calls++ }

func TestWorkflowReleaseWakesWorkerClaimerAfterCommit(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "store.db")
	testdbseed.InsertSession(t, sqlDB, "session-release", testdbseed.DefaultProjectID)
	store := workflowpersistence.New(sqlDB)
	notifier := &countingRunnableNotifier{}
	store.Transactions.SetWorkerRunnableNotifier(notifier)
	run := &api.WorkflowRun{
		SessionID: "session-release", WorkflowID: "plan", WorkflowVersion: "1",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "work",
	}
	testutil.FailErr(t, "create workflow run", store.State.CreateState(t.Context(), run, "", nil))
	run.UpdatedAt = time.Now().UTC()
	err := store.Commands.CommitCommand(t.Context(), run, runstate.CommandMutation{
		OperationID: "release-operation", Kind: "resume", InputDigest: "release-digest",
		Workers: runstate.WorkerMutation{ReleaseHeld: true},
	})
	testutil.FailErr(t, "commit workflow release", err)
	if notifier.calls != 1 {
		t.Fatalf("runnable notifications = %d want 1", notifier.calls)
	}
}
