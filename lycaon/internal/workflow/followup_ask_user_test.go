package workflow

import (
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestFollowUpQuestionSurvivesWorkflowManagerRestart(t *testing.T) {
	mgr, sessions, _, _ := testManagerWithRegistry(t)
	ctx := workflowCaller(t, mgr)
	original, err := mgr.StartAmbient(ctx, "sess-1", "implement", "1.0.0")
	testutil.FailErr(t, "start original workflow", err)
	original.Status = api.WorkflowRunStatusComplete
	completedAt := time.Now().UTC()
	original.CompletedAt = &completedAt
	testutil.FailErr(t, "persist completed workflow", mgr.Store.Update(ctx, original))

	recovered := NewManager(mgr.Store, sessions, mgr.Manifests, nil)
	text, _, handled, err := recovered.PrepareUserRequest(ctx, "sess-1", "Build a new game")
	testutil.FailErr(t, "prepare follow-up after restart", err)
	if handled || text != "Build a new game" {
		t.Fatalf("follow-up = %q, handled = %v", text, handled)
	}
	ask, err := recovered.RequestUserInput(ctx, "sess-1", UserInputRequest{
		Prompt: "Which platform?", ResponseType: workflowdef.FeedbackResponseSingleChoice,
		Options: []string{"macOS", "iOS"}, ToolCallID: "platform-question",
	})
	testutil.FailErr(t, "open follow-up question", err)
	if ask.RunID == original.ID {
		t.Fatal("question attached to completed history")
	}

	// The second restart reloads the pending question from persisted state.
	restarted := NewManager(mgr.Store, sessions, mgr.Manifests, nil)
	active, err := restarted.EnsureSessionWorkflow(ctx, "sess-1")
	testutil.FailErr(t, "retain pending workflow after restart", err)
	if active == nil || active.ID != ask.RunID {
		t.Fatalf("pending workflow replaced: %+v", active)
	}
	ui, err := restarted.ComputeRunUI(ctx, active)
	testutil.FailErr(t, "restore question projection", err)
	if ui.PendingFeedback == nil || ui.PendingFeedback.PhaseID != ask.PhaseID {
		t.Fatalf("restored question = %+v", ui.PendingFeedback)
	}
	_, err = restarted.ResolveUserDecision(ctx, "sess-1", active.ID, ask.PhaseID, []string{"macOS"}, "")
	testutil.FailErr(t, "answer restored question", err)
	vars, err := restarted.Store.GetScaffoldVars(ctx, active.ID)
	testutil.FailErr(t, "load answered question", err)
	if _, pending := coordinatorAskPendingFromVars(vars); pending {
		t.Fatal("restored question remains pending after answer")
	}
}

func TestFollowUpDoesNotReplacePausedWorkflow(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	ctx := t.Context()
	original, err := mgr.StartAmbient(ctx, "sess-1", "implement", "1.0.0")
	testutil.FailErr(t, "start workflow", err)
	_, err = mgr.Pause(ctx, original.ID, "Human paused work")
	testutil.FailErr(t, "pause workflow", err)
	_, _, _, err = mgr.PrepareUserRequest(ctx, "sess-1", "Continue work")
	testutil.FailErr(t, "prepare request against paused workflow", err)
	active, err := mgr.GetActive(ctx, "sess-1")
	testutil.FailErr(t, "load paused workflow", err)
	if active == nil || active.ID != original.ID || active.Status != api.WorkflowRunStatusPaused {
		t.Fatalf("follow-up replaced or resumed the paused workflow: %+v", active)
	}
	if err := mgr.AssertSessionRunnable(ctx, "sess-1"); err == nil {
		t.Fatal("paused workflow admitted model execution")
	}
}
