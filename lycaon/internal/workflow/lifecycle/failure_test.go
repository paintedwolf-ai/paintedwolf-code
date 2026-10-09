package lifecycle_test

import (
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

func TestFailCommitsStructuredTerminalState(t *testing.T) {
	mgr, sessions, _, projectDir := testManager(t)
	run := &api.WorkflowRun{
		SessionID: "sess-1", WorkflowID: "plan", WorkflowVersion: "1.0.0",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "research",
	}
	testutil.FailErr(t, "create run", mgr.Store.State.CreateState(t.Context(), run, projectDir, nil))

	failure := api.WorkflowFailure{
		Code: "TOPOLOGY_STAGE_FAILED", Message: "The workflow topology could not complete.",
		Phase: "research", Stage: "hunt-tests", Retryable: false,
	}
	failed, err := mgr.Controls.Fail(t.Context(), run.ID, failure)
	testutil.FailErr(t, "fail run", err)
	if failed.Status != api.WorkflowRunStatusFailed || !runstate.IsTerminal(failed.Status) {
		t.Fatalf("status = %q terminal = %v", failed.Status, runstate.IsTerminal(failed.Status))
	}
	if failed.Failure == nil || *failed.Failure != failure {
		t.Fatalf("failure = %+v want %+v", failed.Failure, failure)
	}
	if failed.CompletedAt == nil || failed.EndMessageID == "" {
		t.Fatalf("terminal timestamps/boundary missing: %+v", failed)
	}

	stored, err := mgr.Store.Runs.Get(t.Context(), run.ID)
	testutil.FailErr(t, "reload failed run", err)
	if stored.Failure == nil || *stored.Failure != failure {
		t.Fatalf("stored failure = %+v want %+v", stored.Failure, failure)
	}
	running, err := mgr.Store.Runs.ListRunning(t.Context())
	testutil.FailErr(t, "list running", err)
	if len(running) != 0 {
		t.Fatalf("running runs = %+v", running)
	}
	msgs, err := sessions.GetMessages(t.Context(), run.SessionID)
	testutil.FailErr(t, "load failure boundary", err)
	for _, msg := range msgs {
		if msg.WorkflowBoundary != nil && msg.WorkflowBoundary.Event == string(api.WorkflowBoundaryKindFailed) {
			return
		}
	}
	t.Fatal("failed workflow boundary not committed")
}

func TestFailedChildReleasesPausedParent(t *testing.T) {
	mgr, _, _, projectDir := testManager(t)
	parent := &api.WorkflowRun{
		SessionID: "sess-1", WorkflowID: "plan", WorkflowVersion: "1.0.0",
		Status: api.WorkflowRunStatusPausedOnChild, CurrentPhase: "execute",
	}
	testutil.FailErr(t, "create parent", mgr.Store.State.CreateState(t.Context(), parent, projectDir, nil))
	child := &api.WorkflowRun{
		SessionID: "sess-1", WorkflowID: "plan", WorkflowVersion: "1.0.0",
		Status: api.WorkflowRunStatusRunning, CurrentPhase: "research", ParentRunID: &parent.ID,
	}
	testutil.FailErr(t, "create child", mgr.Store.State.CreateState(t.Context(), child, projectDir, nil))

	_, err := mgr.Controls.Fail(t.Context(), child.ID, api.WorkflowFailure{
		Code: "TOPOLOGY_STAGE_FAILED", Message: "The workflow topology could not complete.", Stage: "research",
	})
	testutil.FailErr(t, "fail child", err)
	resumed, err := mgr.Store.Runs.Get(t.Context(), parent.ID)
	testutil.FailErr(t, "load parent", err)
	if resumed.Status != api.WorkflowRunStatusRunning {
		t.Fatalf("parent status = %q want running", resumed.Status)
	}
	vars, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), parent.ID)
	testutil.FailErr(t, "load parent vars", err)
	childRun, _ := vars["child_run"].(map[string]any)
	if childRun["status"] != string(api.WorkflowRunStatusFailed) {
		t.Fatalf("child_run vars = %+v", childRun)
	}
}

func TestRecoverTerminalChildrenReleasesPausedParent(t *testing.T) {
	for _, status := range []api.WorkflowRunStatus{api.WorkflowRunStatusFailed, api.WorkflowRunStatusCanceled, api.WorkflowRunStatusInterrupted} {
		t.Run(string(status), func(t *testing.T) {
			mgr, _, _, projectDir := testManager(t)
			parent := &api.WorkflowRun{
				SessionID: "sess-1", WorkflowID: "plan", WorkflowVersion: "1.0.0",
				Status: api.WorkflowRunStatusPausedOnChild, CurrentPhase: "execute",
			}
			testutil.FailErr(t, "create parent", mgr.Store.State.CreateState(t.Context(), parent, projectDir, nil))
			child := &api.WorkflowRun{
				SessionID: "sess-1", WorkflowID: "plan", WorkflowVersion: "1.0.0",
				Status: api.WorkflowRunStatusRunning, CurrentPhase: "research", ParentRunID: &parent.ID,
			}
			testutil.FailErr(t, "create child", mgr.Store.State.CreateState(t.Context(), child, projectDir, nil))

			now := time.Now().UTC()
			child.Status = status
			child.CompletedAt = &now
			if status == api.WorkflowRunStatusFailed {
				child.Failure = &api.WorkflowFailure{
					Code: "TOPOLOGY_EXECUTION_FAILED", Message: "The workflow topology could not complete.", Retryable: false,
				}
			}
			testutil.FailErr(t, "commit terminal child", mgr.Store.State.Update(t.Context(), child))
			testutil.FailErr(t, "recover terminal child", mgr.Children.RecoverTerminalChildren(t.Context()))

			resumed, err := mgr.Store.Runs.Get(t.Context(), parent.ID)
			testutil.FailErr(t, "load recovered parent", err)
			if resumed.Status != api.WorkflowRunStatusRunning {
				t.Fatalf("parent status = %q want running", resumed.Status)
			}
			vars, err := mgr.Store.Runs.GetScaffoldVars(t.Context(), parent.ID)
			testutil.FailErr(t, "load recovered parent vars", err)
			childRun, _ := vars["child_run"].(map[string]any)
			if childRun["status"] != string(status) {
				t.Fatalf("child_run vars = %+v", childRun)
			}
		})
	}
}
