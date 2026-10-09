package inputs_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowinputs "github.com/lycaon/lycaon/internal/workflow/inputs"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
)

func TestPendingFeedbackFromVarsIncludesChoice(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"ask-user-host@1.0.0": askUserTestManifest(),
	})
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "ask-user-host", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	handle, err := mgr.Asks.RequestUserInput(ctx, "sess-1", workflowinputs.UserInputRequest{
		Prompt:       "Pick a color",
		ResponseType: workflowdef.FeedbackResponseSingleChoice,
		Options:      []string{"red", "green"},
	})
	testutil.FailErr(t, "RequestUserInput", err)

	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	pf, ok := runstate.PendingFeedbackFromVars(vars)
	if !ok || pf.PhaseID != handle.PhaseID || pf.Prompt != "Pick a color" {
		t.Fatalf("runstate.PendingFeedbackFromVars = %+v ok=%v", pf, ok)
	}
	// Response type selects the answer endpoint; options define the available choices.
	if pf.ResponseType != string(workflowdef.FeedbackResponseSingleChoice) {
		t.Fatalf("response_type = %q want single_choice", pf.ResponseType)
	}
	if len(pf.Options) != 2 || pf.Options[0] != "red" || pf.Options[1] != "green" {
		t.Fatalf("options = %v want [red green]", pf.Options)
	}
	ui, err := mgr.Presentation.ComputeRunUI(ctx, run)
	testutil.FailErr(t, "ComputeRunUI", err)
	if ui == nil || ui.PendingFeedback == nil || ui.PendingFeedback.PhaseID != handle.PhaseID {
		t.Fatalf("run UI pending_feedback = %+v", ui)
	}
	if ui.PendingFeedback.ResponseType != string(workflowdef.FeedbackResponseSingleChoice) || len(ui.PendingFeedback.Options) != 2 {
		t.Fatalf("run UI pending_feedback missing choice meta: %+v", ui.PendingFeedback)
	}
}
func TestPendingFeedbackFromVarsTextResponseType(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{
		"ask-user-host@1.0.0": askUserTestManifest(),
	})
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "ask-user-host", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	handle, err := mgr.Asks.RequestUserInput(ctx, "sess-1", workflowinputs.UserInputRequest{
		Prompt:       "Describe the goal",
		ResponseType: workflowdef.FeedbackResponseText,
	})
	testutil.FailErr(t, "RequestUserInput", err)

	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	pf, ok := runstate.PendingFeedbackFromVars(vars)
	if !ok || pf.PhaseID != handle.PhaseID {
		t.Fatalf("runstate.PendingFeedbackFromVars = %+v ok=%v", pf, ok)
	}
	if pf.ResponseType != string(workflowdef.FeedbackResponseText) || len(pf.Options) != 0 {
		t.Fatalf("text ask projection = %+v want response_type=text, no options", pf)
	}
}
