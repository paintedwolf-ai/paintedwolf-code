package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestTwoManifestsShareTopologyStageComplete(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	eval := RegistryGateEvaluator{Registry: reg}
	vars := map[string]any{
		"topology_stages": map[string]any{
			"research": map[string]any{"complete": true},
			"plan":     map[string]any{"complete": true},
		},
	}
	run := &api.WorkflowRun{CurrentPhase: "research", SessionID: "s1", Status: api.WorkflowRunStatusRunning}

	planManifest := workflowdef.Manifest{
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:                "research",
			CompleteWhen:      "topology_stage_complete",
			BindTopologyStage: "research",
		}},
	}
	ok, _, err := eval.PhaseGateMet(context.Background(), planManifest, run, vars)
	if err != nil || !ok {
		t.Fatalf("plan research gate = %v err=%v", ok, err)
	}

	pipelineManifest := workflowdef.Manifest{
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:                "plan",
			CompleteWhen:      "topology_stage_complete",
			BindTopologyStage: "plan",
		}},
	}
	run.CurrentPhase = "plan"
	ok, _, err = eval.PhaseGateMet(context.Background(), pipelineManifest, run, vars)
	if err != nil || !ok {
		t.Fatalf("pipeline plan gate = %v err=%v", ok, err)
	}
}

func TestUserDecisionRejectPausesRun(t *testing.T) {
	mgr, store, _, _ := testManager(t)
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	mgr.SetConditionRegistry(reg)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "decision-flow",
		Version: "1.0.0",
		Controls: workflowdef.ManifestControls{
			OnDecisionReject: &workflowdef.DecisionRejectControls{Pause: true},
		},
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:           "confirm",
			CompleteWhen: "user_decision:confirm,yes",
			OnEnter: workflowdef.PhaseOnEnter{
				RequestUserFeedback: &workflowdef.UserFeedbackPrompt{
					Prompt:       "Proceed?",
					ResponseType: workflowdef.FeedbackResponseSingleChoice,
					Options:      []string{"yes", "no"},
				},
			},
		}},
	})
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"decision-flow@1.0.0": manifest})
	ctx := workflowCaller(t, mgr)
	run, err := startRun(ctx, mgr, "sess-1", "decision-flow", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run, err = mgr.ResolveUserDecision(ctx, "sess-1", run.ID, "confirm", []string{"no"}, "")
	testutil.FailErr(t, "mgr.ResolveUserDecision failed", err)
	if run.Status != api.WorkflowRunStatusPaused {
		t.Fatalf("status = %q want paused", run.Status)
	}
	_ = store
}

func TestUserFeedbackFromChat(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	mgr.SetConditionRegistry(reg)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "feedback-flow",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:           "clarify",
			CompleteWhen: "user_feedback_received:clarify",
			OnEnter: workflowdef.PhaseOnEnter{
				RequestUserFeedback: &workflowdef.UserFeedbackPrompt{Prompt: "Which API?"},
			},
		}},
	})
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"feedback-flow@1.0.0": manifest})
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "feedback-flow", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	if err := mgr.TryResolveUserFeedback(ctx, "sess-1", "", testutil.HostOwner().ID, "GraphQL"); err != nil {
		testutil.FailErr(t, "mgr.TryResolveUserFeedback failed", err)
	}
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "mgr.Store.GetScaffoldVars failed", err)
	eval := RegistryGateEvaluator{Registry: reg, Sessions: mgr.Sessions}
	ok, _, err := eval.PhaseGateMet(ctx, manifest, run, vars)
	if err != nil || !ok {
		t.Fatalf("feedback gate = %v err=%v", ok, err)
	}
}

func TestAdvanceBlockedUntilTopologyStageComplete(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	mgr.SetConditionRegistry(reg)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "pipeline-gate",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:                "implement",
			CompleteWhen:      "topology_stage_complete",
			BindTopologyStage: "implement",
			Next:              "closeout",
		}, {
			ID: "closeout",
		}},
	})
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"pipeline-gate@1.0.0": manifest})
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "pipeline-gate", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	if _, err := mgr.Advance(ctx, run.ID); err == nil {
		t.Fatal("expected gate block before topology stage complete")
	}
	if err := mgr.MarkTopologyStageComplete(ctx, run.ID, "implement", "", ""); err != nil {
		testutil.FailErr(t, "mgr.MarkTopologyStageComplete failed", err)
	}
	run, err = mgr.Advance(ctx, run.ID)
	if err != nil {
		t.Fatalf("advance after stage complete: %v", err)
	}
	if run.CurrentPhase != "closeout" {
		t.Fatalf("phase = %q want closeout", run.CurrentPhase)
	}
}
