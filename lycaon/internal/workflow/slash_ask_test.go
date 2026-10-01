package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func slashAskFeedbackManifest(trigger string) workflowdef.Manifest {
	return workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "slash-ask",
		Version: "1.0.0",
		Trigger: trigger,
		Request: &workflowdef.ManifestRequest{Cadence: workflowdef.RequestCadenceOnce, Question: "What decision are we making?"},
		PhaseDefs: []workflowdef.PhaseDef{{
			ID: "work", CompleteWhen: "gates_satisfied", Gates: []string{"research_satisfied"}, Next: "done",
		}, {ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"}},
	})
}

func TestSlashAttachedRequestDoesNotOpenWorkflowQuestion(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry", err)
	mgr.SetConditionRegistry(reg)
	manifest := slashAskFeedbackManifest("/decide")
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"slash-ask@1.0.0": manifest})

	ctx := context.Background()
	run, err := mgr.startHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "slash-ask", WorkflowVersion: "1.0.0", Request: "pick postgres over sqlite",
	}, "/decide pick postgres over sqlite")
	testutil.FailErr(t, "startHuman with slash ask", err)
	if run.CurrentPhase != "work" {
		t.Fatalf("phase = %q want work", run.CurrentPhase)
	}
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if feedbackPending(vars, workflowRequestFeedbackID) {
		t.Fatal("attached request opened a workflow question")
	}
	request, ok := requestStateFromVars(vars)
	if !ok || request.Text != "pick postgres over sqlite" || request.Source != "explicit" {
		t.Fatalf("request = %+v ok=%v", request, ok)
	}
}

func TestBareSlashOpensWorkflowQuestion(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry", err)
	mgr.SetConditionRegistry(reg)
	manifest := slashAskFeedbackManifest("/decide")
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"slash-ask@1.0.0": manifest})

	ctx := context.Background()
	run, err := mgr.startHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "slash-ask", WorkflowVersion: "1.0.0",
	}, "/decide")
	testutil.FailErr(t, "startHuman bare slash", err)
	if run.CurrentPhase != "work" {
		t.Fatalf("phase = %q want work", run.CurrentPhase)
	}
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if !feedbackPending(vars, workflowRequestFeedbackID) || !requestPending(vars) {
		t.Fatal("workflow request not pending")
	}
}

func TestSlashAskDoesNotResolveChoiceFeedback(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry", err)
	mgr.SetConditionRegistry(reg)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:       "slash-choice",
		Version:  "1.0.0",
		Trigger:  "/choose",
		Request:  &workflowdef.ManifestRequest{Cadence: workflowdef.RequestCadenceOnce, Question: "What should we choose?", Default: "Choose the best API."},
		Controls: workflowdef.ManifestControls{PhaseAdvance: workflowdef.PhaseAdvanceHost},
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:           "pick",
			CompleteWhen: "user_decision_received:pick",
			Next:         "done",
			OnEnter: workflowdef.PhaseOnEnter{
				RequestUserFeedback: &workflowdef.UserFeedbackPrompt{
					Prompt:       "Which API?",
					ResponseType: workflowdef.FeedbackResponseSingleChoice,
					Options:      []string{"REST", "GraphQL"},
				},
			},
		}, {ID: "done", Terminal: true, CompleteWhen: "orchestration_complete"}},
	})
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"slash-choice@1.0.0": manifest})

	ctx := context.Background()
	run, err := mgr.startHuman(ctx, "sess-1", api.StartWorkflowRunRequest{
		WorkflowID: "slash-choice", WorkflowVersion: "1.0.0",
	}, "/choose REST")
	testutil.FailErr(t, "startHuman choice slash", err)
	if run.CurrentPhase != "pick" {
		t.Fatalf("phase = %q want pick", run.CurrentPhase)
	}
	vars, err := mgr.Store.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	if !decisionPending(vars, "pick") {
		t.Fatal("choice feedback not pending")
	}
}
