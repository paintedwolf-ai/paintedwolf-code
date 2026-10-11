package inputs_test

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestResolveUserFeedbackValidatesPending(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	mgr.SetConditionRegistry(reg)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "feedback-resolve",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:           "clarify",
			CompleteWhen: "user_feedback_received:clarify",
			OnEnter: workflowdef.PhaseOnEnter{
				RequestUserFeedback: &workflowdef.UserFeedbackPrompt{Prompt: "Which API?"},
			},
		}},
	})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"feedback-resolve@1.0.0": manifest})
	ctx := workflowCaller(t, mgr)
	run, err := startRun(ctx, mgr, "sess-1", "feedback-resolve", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	if _, err := mgr.Feedback.ResolveUserFeedback(ctx, "sess-1", run.ID, "wrong-phase", "REST"); !errors.Is(err, runstate.ErrFeedbackNotPending) {
		t.Fatalf("err = %v want runstate.ErrFeedbackNotPending", err)
	}
	if _, err := mgr.Feedback.ResolveUserFeedback(ctx, "sess-1", run.ID, "clarify", "   "); !errors.Is(err, runstate.ErrFeedbackEmptyResponse) {
		t.Fatalf("err = %v want runstate.ErrFeedbackEmptyResponse", err)
	}
}

func TestResolveUserFeedbackTriggersAutoAdvance(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	mgr.SetConditionRegistry(reg)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:       "feedback-advance",
		Version:  "1.0.0",
		Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:           "clarify",
			CompleteWhen: "user_feedback_received:clarify",
			Next:         "done",
			OnEnter: workflowdef.PhaseOnEnter{
				RequestUserFeedback: &workflowdef.UserFeedbackPrompt{Prompt: "Proceed?"},
			},
		}, {ID: "done"}},
	})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"feedback-advance@1.0.0": manifest})
	ctx := workflowCaller(t, mgr)
	run, err := startRun(ctx, mgr, "sess-1", "feedback-advance", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run, err = mgr.Feedback.ResolveUserFeedback(ctx, "sess-1", run.ID, "clarify", "yes go ahead")
	testutil.FailErr(t, "mgr.Feedback.ResolveUserFeedback failed", err)
	if run.CurrentPhase != "done" {
		t.Fatalf("phase = %q want done", run.CurrentPhase)
	}
	if run.Status != api.WorkflowRunStatusRunning && run.Status != api.WorkflowRunStatusComplete {
		t.Fatalf("status = %q", run.Status)
	}
}

func TestResolveUserFeedbackRejectsStaleReviewedRevision(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry", err)
	mgr.SetConditionRegistry(reg)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID: "feedback-revision", Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID: "clarify", CompleteWhen: "user_feedback_received:clarify",
			OnEnter: workflowdef.PhaseOnEnter{RequestUserFeedback: &workflowdef.UserFeedbackPrompt{Prompt: "Which API?"}},
		}},
	})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"feedback-revision@1.0.0": manifest})
	ctx := workflowCaller(t, mgr)
	run, err := startRun(ctx, mgr, "sess-1", "feedback-revision", "1.0.0")
	testutil.FailErr(t, "startRun", err)
	staleRevision := run.Revision
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars["concurrent_change"] = true
	testutil.FailErr(t, "UpdateVars", mgr.Store.State.UpdateVars(ctx, run, "", vars))

	_, err = mgr.Feedback.ResolveUserFeedback(runstate.WithExpectedRevision(ctx, staleRevision), "sess-1", run.ID, "clarify", "REST")
	if !errors.Is(err, runstate.ErrRevisionConflict) {
		t.Fatalf("ResolveUserFeedback error = %v, want runstate.ErrRevisionConflict", err)
	}
	got, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars after conflict", err)
	if !runstate.FeedbackPending(got, "clarify") {
		t.Fatal("stale feedback command cleared the pending input")
	}
}
