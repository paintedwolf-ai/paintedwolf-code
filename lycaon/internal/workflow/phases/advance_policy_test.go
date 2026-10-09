package phases_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	workflow "github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestImplementChildCompletesFromDeliveredWorkWithoutTests(t *testing.T) {
	reg, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	manifest, err := reg.Get("implement", "1.0.0")
	testutil.FailErr(t, "Get implement", err)

	conditionRegistry, err := conditions.NewDefaultRegistry(conditions.RegistryDeps{
		DelegationCloseout: func(context.Context, string) (bool, error) { return true, nil },
		DeliveryReported:   func(context.Context, string, string, string) (bool, error) { return true, nil },
	})
	testutil.FailErr(t, "NewDefaultRegistry", err)
	parentID := "parent-run"
	run := &api.WorkflowRun{
		ID:           "child-run",
		SessionID:    "sess-1",
		ParentRunID:  &parentID,
		CurrentPhase: "work",
	}

	ok, result, err := (workflow.RegistryGateEvaluator{Registry: conditionRegistry}).PhaseGateMet(t.Context(), manifest, run, nil)
	testutil.FailErr(t, "PhaseGateMet", err)
	if !ok || result.FailedGate != "" {
		t.Fatalf("reported delivery must satisfy child work: ok=%v result=%+v", ok, result)
	}
}

func TestTryAutoAdvanceCoordinatorPolicyParks(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := t.Context()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	if run.CurrentPhase != "research" {
		t.Fatalf("phase after intake = %q want research", run.CurrentPhase)
	}

	advanced, err := mgr.Phases.TryAutoAdvance(ctx, run.ID)
	testutil.FailErr(t, "TryAutoAdvance", err)
	if advanced.CurrentPhase != "research" {
		t.Fatalf("coordinator-advance must park on research; phase = %q", advanced.CurrentPhase)
	}
}

func TestPlanResearchCoordinatorAdvanceRecipe(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	reg := registerAdvanceToolTestRegistry(t, mgr)
	ctx := context.Background()

	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	if run.CurrentPhase != "research" {
		t.Fatalf("phase after intake = %q want research", run.CurrentPhase)
	}

	advanced, err := mgr.Phases.TryAutoAdvance(ctx, run.ID)
	testutil.FailErr(t, "TryAutoAdvance", err)
	if advanced.CurrentPhase != "research" {
		t.Fatalf("TryAutoAdvance must not leave research; phase = %q", advanced.CurrentPhase)
	}

	// research_satisfied is coordinator-controlled; stamp then workflow_advance leaves.
	vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars["research_satisfied"] = true
	if err := mgr.Store.State.UpdateVars(ctx, run, projectDir, vars); err != nil {
		testutil.FailErr(t, "UpdateVars research_satisfied", err)
	}

	result := runAdvanceTool(t, reg, projectDir)
	if result.Error != "" {
		t.Fatalf("workflow_advance on coordinator research: error=%q result=%+v", result.Error, result)
	}
	if result.Run == nil || result.Run.CurrentPhase != "expand" {
		t.Fatalf("workflow_advance should advance research→expand; run=%+v", result.Run)
	}
}
