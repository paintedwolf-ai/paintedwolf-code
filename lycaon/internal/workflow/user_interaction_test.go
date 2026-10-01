package workflow

import (
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
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
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"feedback-resolve@1.0.0": manifest})
	ctx := workflowCaller(t, mgr)
	run, err := startRun(ctx, mgr, "sess-1", "feedback-resolve", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	if _, err := mgr.ResolveUserFeedback(ctx, "sess-1", run.ID, "wrong-phase", "REST"); !errors.Is(err, ErrFeedbackNotPending) {
		t.Fatalf("err = %v want ErrFeedbackNotPending", err)
	}
	if _, err := mgr.ResolveUserFeedback(ctx, "sess-1", run.ID, "clarify", "   "); !errors.Is(err, ErrFeedbackEmptyResponse) {
		t.Fatalf("err = %v want ErrFeedbackEmptyResponse", err)
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
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"feedback-advance@1.0.0": manifest})
	ctx := workflowCaller(t, mgr)
	run, err := startRun(ctx, mgr, "sess-1", "feedback-advance", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run, err = mgr.ResolveUserFeedback(ctx, "sess-1", run.ID, "clarify", "yes go ahead")
	testutil.FailErr(t, "mgr.ResolveUserFeedback failed", err)
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
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"feedback-revision@1.0.0": manifest})
	ctx := workflowCaller(t, mgr)
	run, err := startRun(ctx, mgr, "sess-1", "feedback-revision", "1.0.0")
	testutil.FailErr(t, "startRun", err)
	staleRevision := run.Revision
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars["concurrent_change"] = true
	testutil.FailErr(t, "UpdateVars", mgr.Store.UpdateVars(ctx, run, "", vars))

	_, err = mgr.ResolveUserFeedback(WithExpectedRevision(ctx, staleRevision), "sess-1", run.ID, "clarify", "REST")
	if !errors.Is(err, ErrRunRevisionConflict) {
		t.Fatalf("ResolveUserFeedback error = %v, want ErrRunRevisionConflict", err)
	}
	got, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars after conflict", err)
	if !feedbackPending(got, "clarify") {
		t.Fatal("stale feedback command cleared the pending input")
	}
}
