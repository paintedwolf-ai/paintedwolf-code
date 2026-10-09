package inputs_test

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"testing"
)

// A multi_choice phase: the choice-branch gate (user_decision:pick,blue) is satisfied by
// membership in the selected set, the run advances, and the question card records the answer.
func TestResolveMultiChoiceAdvancesAndStampsAnswer(t *testing.T) {
	mgr, store, _, _ := testManager(t)
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry", err)
	mgr.SetConditionRegistry(reg)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "choice-flow",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{
			{
				ID:                 "pick",
				CompleteWhen:       "user_decision:pick,blue",
				AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetAuto,
				OnEnter: workflowdef.PhaseOnEnter{RequestUserFeedback: &workflowdef.UserFeedbackPrompt{
					Prompt:       "Pick colors",
					ResponseType: workflowdef.FeedbackResponseMultiChoice,
					Options:      []string{"red", "blue", "green"},
					AllowOther:   true,
				}},
				Next: "done",
			},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"choice-flow@1.0.0": manifest})
	ctx := workflowCaller(t, mgr)
	run, err := startRun(ctx, mgr, "sess-1", "choice-flow", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	// Select two options plus a free-text Other value.
	run, err = mgr.Feedback.ResolveUserDecision(ctx, "sess-1", run.ID, "pick", []string{"blue", "green", "teal"}, "")
	testutil.FailErr(t, "ResolveUserDecision", err)
	if run.CurrentPhase != "done" {
		t.Fatalf("phase = %q want done (gate user_decision:pick,blue should match via membership)", run.CurrentPhase)
	}

	msgs, err := store.GetMessages(ctx, "sess-1")
	testutil.FailErr(t, "GetMessages", err)
	fb := feedbackMessages(msgs)
	if len(fb) != 1 || fb[0].WorkflowFeedback == nil {
		t.Fatalf("want 1 feedback message with meta, got %d", len(fb))
	}
	if fb[0].WorkflowFeedback.Answer != "blue, green, teal" {
		t.Fatalf("answer = %q want %q", fb[0].WorkflowFeedback.Answer, "blue, green, teal")
	}

	// A stale/double submit on the already-resolved phase is a clean conflict, not a 500.
	if _, err := mgr.Feedback.ResolveUserDecision(ctx, "sess-1", run.ID, "pick", []string{"red"}, ""); !errors.Is(err, runstate.ErrDecisionNotPending) {
		t.Fatalf("double resolve err = %v want runstate.ErrDecisionNotPending", err)
	}
}

// allow_other lets a single_choice phase accept a value outside the declared options.
func TestResolveSingleChoiceRejectsUnknownWithoutAllowOther(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry", err)
	mgr.SetConditionRegistry(reg)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "single-flow",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{
			{
				ID:           "pick",
				CompleteWhen: "user_decision_received:pick",
				OnEnter: workflowdef.PhaseOnEnter{RequestUserFeedback: &workflowdef.UserFeedbackPrompt{
					Prompt:       "Pick one",
					ResponseType: workflowdef.FeedbackResponseSingleChoice,
					Options:      []string{"red", "blue"},
				}},
				Next: "done",
			},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"single-flow@1.0.0": manifest})
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "single-flow", "1.0.0")
	testutil.FailErr(t, "startRun", err)
	if _, err := mgr.Feedback.ResolveUserDecision(ctx, "sess-1", run.ID, "pick", []string{"purple"}, ""); err == nil {
		t.Fatal("expected rejection of non-option choice when allow_other is false")
	}
	if _, err := mgr.Feedback.ResolveUserDecision(ctx, "sess-1", run.ID, "pick", []string{"blue", "red"}, ""); err == nil {
		t.Fatal("expected single_choice to reject multiple selections")
	}
}
