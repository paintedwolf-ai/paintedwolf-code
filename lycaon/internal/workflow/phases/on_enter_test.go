package phases_test

import (
	"context"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/scaffoldvars"
	"github.com/lycaon/lycaon/internal/session/store"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestApplyPhaseOnEnterKeepsPostureAsPostCommitEffect(t *testing.T) {
	sqlDB := testdbfixture.Open(t, "onenter.db")
	sessStore := store.NewSQL(sqlDB)
	testdbseed.InsertProject(t, sqlDB, testdbseed.DefaultProjectID)
	testdbseed.InsertDraftScratchRoot(t, sqlDB, testdbseed.DefaultProjectID)
	testdbseed.InsertSession(t, sqlDB, "s1", testdbseed.DefaultProjectID)
	manifest := workflowdef.Manifest{
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "implement", OnEnter: workflowdef.PhaseOnEnter{SetPosture: "build"}},
		},
	}
	before, err := sessStore.Get(context.Background(), "s1")
	testutil.FailErr(t, "sessStore.Get before", err)
	vars, err := workflowphases.ApplyPhaseOnEnter(context.Background(), workflowphases.PhaseEnterRequest{
		Sessions: sessStore, SessionID: "s1", Manifest: manifest, PhaseID: "implement",
	})
	testutil.FailErr(t, "workflowphases.ApplyPhaseOnEnter failed", err)
	if vars == nil {
		t.Fatal("expected vars map")
	}
	sess, err := sessStore.Get(context.Background(), "s1")
	testutil.FailErr(t, "sessStore.Get failed", err)
	if sess.Posture != before.Posture {
		t.Fatalf("pure phase preparation changed posture from %q to %q", before.Posture, sess.Posture)
	}
}

func TestApplyPhaseOnEnterUserFeedback(t *testing.T) {
	manifest := workflowdef.Manifest{
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "clarify", OnEnter: workflowdef.PhaseOnEnter{RequestUserFeedback: &workflowdef.UserFeedbackPrompt{Prompt: "Which API?"}}},
		},
	}
	vars, err := workflowphases.ApplyPhaseOnEnter(context.Background(), workflowphases.PhaseEnterRequest{Manifest: manifest, PhaseID: "clarify"})
	testutil.FailErr(t, "workflowphases.ApplyPhaseOnEnter failed", err)
	fb, ok := vars["user_feedback"].(map[string]any)
	if !ok {
		t.Fatal("missing user_feedback bucket")
	}
	entry, ok := fb["clarify"].(map[string]any)
	if !ok || entry["prompt"] != "Which API?" {
		t.Fatalf("feedback = %v", entry)
	}
}

func TestApplyPhaseOnEnterUserDecision(t *testing.T) {
	manifest := workflowdef.Manifest{
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "confirm", OnEnter: workflowdef.PhaseOnEnter{RequestUserFeedback: &workflowdef.UserFeedbackPrompt{
				Prompt:       "Proceed?",
				ResponseType: workflowdef.FeedbackResponseSingleChoice,
				Options:      []string{"yes", "no"},
			}}},
		},
	}
	vars, err := workflowphases.ApplyPhaseOnEnter(context.Background(), workflowphases.PhaseEnterRequest{Manifest: manifest, PhaseID: "confirm"})
	testutil.FailErr(t, "workflowphases.ApplyPhaseOnEnter failed", err)
	dec, ok := vars["user_decision"].(map[string]any)
	if !ok {
		t.Fatal("missing user_decision bucket")
	}
	entry, ok := dec["confirm"].(map[string]any)
	if !ok || entry["prompt"] != "Proceed?" {
		t.Fatalf("decision = %v", entry)
	}
}

func TestParseOnEnterInvalidSessionPosture(t *testing.T) {
	_, err := workflowdef.ParseManifestYAML([]byte(`
id: bad
version: 1.0.0
phases:
  - id: research
    activity_label: Test phase
    on_enter:
      set_posture: research
`))
	if err == nil {
		t.Fatal("expected invalid session posture error")
	}
}

func TestApplyPhaseOnEnterSetExecutionModeInvestigate(t *testing.T) {
	manifest := workflowdef.Manifest{
		PhaseDefs: []workflowdef.PhaseDef{{
			ID: "scout", OnEnter: workflowdef.PhaseOnEnter{SetExecutionMode: workflowdef.ExecutionModeInvestigate},
		}},
	}
	vars, err := workflowphases.ApplyPhaseOnEnter(context.Background(), workflowphases.PhaseEnterRequest{Manifest: manifest, PhaseID: "scout"})
	testutil.FailErr(t, "workflowphases.ApplyPhaseOnEnter failed", err)
	mode, ok := workflowdef.ScaffoldExecutionModeStamp(vars)
	if !ok || mode != workflowdef.ExecutionModeInvestigate {
		t.Fatalf("stamp = %q ok=%v", mode, ok)
	}
}

func TestApplyPhaseOnEnterSetExecutionModeStateDerivedClearsStamp(t *testing.T) {
	manifest := workflowdef.Manifest{
		PhaseDefs: []workflowdef.PhaseDef{{
			ID: "work", OnEnter: workflowdef.PhaseOnEnter{SetExecutionMode: workflowdef.ExecutionModeStateDerived},
		}},
	}
	vars, err := workflowphases.ApplyPhaseOnEnter(context.Background(), workflowphases.PhaseEnterRequest{
		Manifest: manifest, PhaseID: "work", Vars: map[string]any{
			"execution_mode":      workflowdef.ExecutionModeInvestigate,
			"host.execution_mode": workflowdef.ExecutionModeInvestigate,
		},
	})
	testutil.FailErr(t, "workflowphases.ApplyPhaseOnEnter failed", err)
	if _, ok := workflowdef.ScaffoldExecutionModeStamp(vars); ok {
		t.Fatal("state_derived must clear execution_mode stamp")
	}
}

func TestApplyPhaseOnEnterInvalidExecutionMode(t *testing.T) {
	manifest := workflowdef.Manifest{
		PhaseDefs: []workflowdef.PhaseDef{{
			ID: "work", OnEnter: workflowdef.PhaseOnEnter{SetExecutionMode: "parallel"},
		}},
	}
	_, err := workflowphases.ApplyPhaseOnEnter(context.Background(), workflowphases.PhaseEnterRequest{Manifest: manifest, PhaseID: "work"})
	if err == nil {
		t.Fatal("expected invalid execution mode error")
	}
}

func TestRefreshHumanApprovalReadyStubOnly(t *testing.T) {
	const lockedReadiness = "plan_stub_valid"
	planBody := conditions.TestPlanContentStubOnly
	reg, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		BlueprintGet: func(_ context.Context, planID string) (*api.Blueprint, error) {
			if planID == "empty-plan" {
				return &api.Blueprint{Path: planID, Content: ""}, nil
			}
			if planID == "valid-plan" {
				return &api.Blueprint{Path: planID, Content: planBody}, nil
			}
			return nil, nil
		},
	})
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)

	def := workflowdef.PhaseDef{
		ID: "approve",
		HumanApproval: &workflowdef.HumanApprovalConfig{
			Readiness: lockedReadiness,
		},
	}

	t.Run("empty plan => not ready", func(t *testing.T) {
		vars := workflowphases.RefreshHumanApprovalReady(context.Background(), reg, def, nil, "empty-plan", "")
		if conditions.DotPathTruthy(vars, "human_approval.ready") {
			t.Fatalf("human_approval.ready = true with empty plan; vars=%v", vars)
		}
	})

	t.Run("valid plan => ready", func(t *testing.T) {
		vars := workflowphases.RefreshHumanApprovalReady(context.Background(), reg, def, nil, "valid-plan", "")
		if !conditions.DotPathTruthy(vars, "human_approval.ready") {
			t.Fatalf("human_approval.ready = false with valid plan; vars=%v", vars)
		}
	})
}

// Phase entry leaves nested maps owned by the caller untouched.
func TestApplyPhaseOnEnterDoesNotMutateSharedNestedVars(t *testing.T) {
	manifest := workflowdef.Manifest{
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "clarify2", OnEnter: workflowdef.PhaseOnEnter{RequestUserFeedback: &workflowdef.UserFeedbackPrompt{Prompt: "Which DB?"}}},
		},
	}
	sharedFeedback := map[string]any{
		"clarify1": map[string]any{"prompt": "Which API?", "pending": true},
	}
	vars := map[string]any{"user_feedback": sharedFeedback}

	_, err := workflowphases.ApplyPhaseOnEnter(context.Background(), workflowphases.PhaseEnterRequest{
		Manifest: manifest, PhaseID: "clarify2", Vars: vars,
	})
	testutil.FailErr(t, "workflowphases.ApplyPhaseOnEnter failed", err)

	if _, ok := sharedFeedback["clarify2"]; ok {
		t.Fatal("applyOnEnter mutated the caller's shared user_feedback map in place")
	}
	if len(sharedFeedback) != 1 {
		t.Fatalf("shared user_feedback bucket unexpectedly grew: %v", sharedFeedback)
	}
}

func TestHumanApprovalAwaitingRequiresReady(t *testing.T) {
	vars := runstate.SetHostVar(nil, "human_approval.active", true)
	vars = runstate.SetHostVar(vars, "human_approval.blueprint_path", "bp.md")
	if scaffoldvars.HumanApprovalAwaiting(vars) {
		t.Fatal("active without ready must not await")
	}
	vars = runstate.SetHumanApprovalReady(vars, true)
	if !scaffoldvars.HumanApprovalAwaiting(vars) {
		t.Fatal("active + ready must await")
	}
}
