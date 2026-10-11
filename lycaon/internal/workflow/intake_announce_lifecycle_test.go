package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// Declared intake announces its pending choice card.
func TestDeclaredIntakeStartAnnouncesChoiceCard(t *testing.T) {
	mgr, store, _, _ := testManagerWithRegistry(t)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "intake-announce",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{
			{
				ID:                 "intake",
				Intake:             []string{"change_size"},
				CompleteWhen:       "gates_satisfied",
				Gates:              []string{"user_decision_received:change_size"},
				AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetAuto,
				Next:               "done",
			},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"intake-announce@1.0.0": manifest})
	ctx := workflowCaller(t, mgr)
	run, err := startRun(ctx, mgr, "sess-1", "intake-announce", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	msgs, err := store.GetMessages(ctx, "sess-1")
	testutil.FailErr(t, "GetMessages", err)
	feedback := feedbackMessages(msgs)
	if len(feedback) != 1 {
		t.Fatalf("want 1 intake card after start, got %d", len(feedback))
	}
	meta := feedback[0].WorkflowFeedback
	if meta == nil || meta.PhaseID != "change_size" {
		t.Fatalf("card phase_id = %#v want change_size", meta)
	}
	if meta.ResponseType != api.FeedbackResponseType(workflowdef.FeedbackResponseSingleChoice) {
		t.Fatalf("response_type = %q want single_choice", meta.ResponseType)
	}
	if len(meta.Options) < 2 {
		t.Fatalf("options = %v want catalog values", meta.Options)
	}

	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if announced, _ := vars["feedback_announced:change_size"].(bool); !announced {
		t.Fatal("expected per-key feedback_announced:change_size marker")
	}

	run, err = mgr.Feedback.ResolveUserDecision(ctx, "sess-1", run.ID, "change_size", []string{"medium"}, "")
	testutil.FailErr(t, "ResolveUserDecision", err)
	if run.CurrentPhase != "done" {
		t.Fatalf("phase = %q want done", run.CurrentPhase)
	}
	if got, _ := runstate.DotPathString(varsAfter(t, mgr, run.ID), "intake.change_size"); got != "medium" {
		t.Fatalf("intake.change_size = %q want medium", got)
	}
}

// Multi-key declared intake announces sequential cards: resolve key1 → card for key2.
func TestDeclaredMultiKeyIntakeSequentialCards(t *testing.T) {
	mgr, store, _, _ := testManagerWithRegistry(t)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "intake-multi",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{
			{
				ID:           "intake",
				Intake:       []string{"change_size", "breaking_change"},
				CompleteWhen: "gates_satisfied",
				Gates: []string{
					"user_decision_received:change_size",
					"user_decision_received:breaking_change",
				},
				AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetAuto,
				Next:               "done",
			},
			{ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"},
		},
	})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"intake-multi@1.0.0": manifest})
	ctx := workflowCaller(t, mgr)
	run, err := startRun(ctx, mgr, "sess-1", "intake-multi", "1.0.0")
	testutil.FailErr(t, "startRun", err)

	msgs, err := store.GetMessages(ctx, "sess-1")
	testutil.FailErr(t, "GetMessages", err)
	feedback := feedbackMessages(msgs)
	if len(feedback) != 1 || feedback[0].WorkflowFeedback == nil || feedback[0].WorkflowFeedback.PhaseID != "change_size" {
		t.Fatalf("first card = %#v want change_size", feedback)
	}

	run, err = mgr.Feedback.ResolveUserDecision(ctx, "sess-1", run.ID, "change_size", []string{"small"}, "")
	testutil.FailErr(t, "ResolveUserDecision change_size", err)
	if run.CurrentPhase != "intake" {
		t.Fatalf("phase after first key = %q want intake", run.CurrentPhase)
	}

	msgs, err = store.GetMessages(ctx, "sess-1")
	testutil.FailErr(t, "GetMessages after first", err)
	feedback = feedbackMessages(msgs)
	if len(feedback) != 2 {
		t.Fatalf("want 2 cards after first resolve, got %d", len(feedback))
	}
	if feedback[1].WorkflowFeedback == nil || feedback[1].WorkflowFeedback.PhaseID != "breaking_change" {
		t.Fatalf("second card = %#v want breaking_change", feedback[1].WorkflowFeedback)
	}

	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if announced, _ := vars["feedback_announced:breaking_change"].(bool); !announced {
		t.Fatal("expected per-key feedback_announced:breaking_change marker")
	}

	run, err = mgr.Feedback.ResolveUserDecision(ctx, "sess-1", run.ID, "breaking_change", []string{"none"}, "")
	testutil.FailErr(t, "ResolveUserDecision breaking_change", err)
	if run.CurrentPhase != "done" {
		t.Fatalf("phase = %q want done", run.CurrentPhase)
	}
}

func varsAfter(t *testing.T, mgr *RunManager, runID string) map[string]any {
	t.Helper()
	vars, err := mgr.Store.Runs.GetScaffoldVars(context.Background(), runID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	return vars
}

func feedbackMessages(msgs []api.Message) []api.Message {
	var out []api.Message
	for _, m := range msgs {
		if m.Kind == api.MessageKindWorkflowFeedback {
			out = append(out, m)
		}
	}
	return out
}
