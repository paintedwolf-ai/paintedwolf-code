package phases_test

import (
	workflow "github.com/lycaon/lycaon/internal/workflow"
	"github.com/lycaon/lycaon/internal/workflow/runstate"

	"context"
	"encoding/json"
	workflowphases "github.com/lycaon/lycaon/internal/workflow/phases"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func registerAdvanceToolTestRegistry(t *testing.T, mgr *workflow.RunManager) *tools.DefaultRegistry {
	t.Helper()
	reg := tools.NewDefaultRegistry()
	if err := workflowphases.RegisterAdvanceTool(reg, mgr.Phases); err != nil {
		testutil.FailErr(t, "workflowphases.RegisterAdvanceTool failed", err)
	}
	return reg
}

func TestWorkflowAdvanceHappyPath(t *testing.T) {
	mgr, _, _, projectDir := testManagerWithRegistry(t)
	reg := registerAdvanceToolTestRegistry(t, mgr)
	ctx := context.Background()
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "adv-coord",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:                 "stub",
			Next:               "research",
			AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetCoordinator,
		}, {ID: "research"}},
	})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"adv-coord@1.0.0": manifest})
	if _, err := startRun(ctx, mgr, "sess-1", "adv-coord", "1.0.0"); err != nil {
		testutil.FailErr(t, "startRun failed", err)
	}

	tctx := toolContext("coordinator", "sess-1", projectDir)
	tctx.Identity.ToolCallID = "advance-call-1"
	out, err := reg.Run(ctx, "workflow_advance", map[string]any{}, tctx)
	testutil.FailErr(t, "reg.Run failed", err)
	var result workflowphases.AdvanceToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if result.Run == nil || result.Run.CurrentPhase != "research" {
		t.Fatalf("result = %+v", result)
	}
	replayed, err := reg.Run(ctx, "workflow_advance", map[string]any{}, tctx)
	testutil.FailErr(t, "replay workflow_advance", err)
	var replayedResult workflowphases.AdvanceToolResult
	testutil.FailErr(t, "unmarshal replay", json.Unmarshal([]byte(replayed), &replayedResult))
	if replayedResult.Run == nil || replayedResult.Run.Revision != result.Run.Revision {
		t.Fatalf("replayed result = %+v want revision %d", replayedResult, result.Run.Revision)
	}
}

func TestWorkflowAdvanceGateUnmet(t *testing.T) {
	mgr, _, _, projectDir := testManagerWithRegistry(t)
	reg := registerAdvanceToolTestRegistry(t, mgr)
	ctx := context.Background()
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "adv-coord",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:                 "stub",
			CompleteWhen:       workflowdef.CompleteWhenGatesSatisfied,
			Gates:              []string{"plan_stub_valid"},
			Next:               "research",
			AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetCoordinator,
		}, {ID: "research"}},
	})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"adv-coord@1.0.0": manifest})
	run, err := startRun(ctx, mgr, "sess-1", "adv-coord", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	_ = run

	out, err := reg.Run(ctx, "workflow_advance", map[string]any{}, toolContext("coordinator", "sess-1", projectDir))
	testutil.FailErr(t, "reg.Run failed", err)
	var result workflowphases.AdvanceToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if result.Error != "phase_gate_unmet" {
		t.Fatalf("error = %q", result.Error)
	}
	if len(result.FailedLeaves) == 0 {
		t.Fatal("expected failed_leaves")
	}
	if result.Phase != "stub" {
		t.Fatalf("phase = %q", result.Phase)
	}
}

func TestWorkflowAdvanceNoActiveRun(t *testing.T) {
	mgr, _, _, projectDir := testManager(t)
	reg := registerAdvanceToolTestRegistry(t, mgr)
	_, err := reg.Run(context.Background(), "workflow_advance", map[string]any{}, toolContext("coordinator", "sess-1", projectDir))
	if err == nil || !strings.Contains(err.Error(), "active") {
		t.Fatalf("err = %v", err)
	}
}

func TestWorkflowAdvanceAlreadyAdvanced(t *testing.T) {
	mgr, _, _, projectDir := testManagerWithRegistry(t)
	reg := registerAdvanceToolTestRegistry(t, mgr)
	ctx := context.Background()
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "adv-already",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:                 "stub",
			CompleteWhen:       workflowdef.CompleteWhenGatesSatisfied,
			Gates:              []string{"plan_stub_valid"},
			Next:               "research",
			AdvanceWhenGateMet: workflowdef.AdvanceWhenGateMetCoordinator,
		}, {ID: "research"}},
	})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"adv-already@1.0.0": manifest})
	run, err := startRun(ctx, mgr, "sess-1", "adv-already", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)

	vars := runstate.SetHostVar(nil, workflowphases.HostAutoAdvancedFromKey, "stub")
	if err := mgr.Store.State.UpdateVars(ctx, run, projectDir, vars); err != nil {
		testutil.FailErr(t, "UpdateVars", err)
	}

	out, err := reg.Run(ctx, "workflow_advance", map[string]any{}, toolContext("coordinator", "sess-1", projectDir))
	testutil.FailErr(t, "reg.Run failed", err)
	var result workflowphases.AdvanceToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if !result.AlreadyAdvanced {
		t.Fatalf("result = %+v", result)
	}
	if result.Run == nil || result.Run.CurrentPhase != "stub" {
		t.Fatalf("run = %+v", result.Run)
	}

	out, err = reg.Run(ctx, "workflow_advance", map[string]any{}, toolContext("coordinator", "sess-1", projectDir))
	testutil.FailErr(t, "reg.Run failed", err)
	var second workflowphases.AdvanceToolResult
	if err := json.Unmarshal([]byte(out), &second); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if second.AlreadyAdvanced {
		t.Fatal("second advance should not be already_advanced")
	}
}

func TestWorkflowAdvanceBlocksOnPendingFeedback(t *testing.T) {
	mgr, _, _, projectDir := testManager(t)
	reg := registerAdvanceToolTestRegistry(t, mgr)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "advance-feedback",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID:           "clarify",
			CompleteWhen: "user_feedback_received:clarify",
			Next:         "done",
			OnEnter: workflowdef.PhaseOnEnter{
				RequestUserFeedback: &workflowdef.UserFeedbackPrompt{Prompt: "Which API?"},
			},
		}, {ID: "done"}},
	})
	mgr.Resolver.Overlay = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"advance-feedback@1.0.0": manifest})
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "advance-feedback", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	_ = run

	out, err := reg.Run(ctx, "workflow_advance", map[string]any{}, toolContext("coordinator", "sess-1", projectDir))
	testutil.FailErr(t, "reg.Run failed", err)
	var result workflowphases.AdvanceToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if result.Error != "pending_user_input" {
		t.Fatalf("error = %q", result.Error)
	}
	if result.PendingPhase != "clarify" {
		t.Fatalf("pending_phase = %q", result.PendingPhase)
	}
}

func TestWorkflowAdvanceRejectsHostAutoPhase(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	reg := registerAdvanceToolTestRegistry(t, mgr)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "complete research at depth none", err)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	_, err = mgr.Phases.TryAutoAdvance(ctx, run.ID)
	testutil.FailErr(t, "TryAutoAdvance expand", err)

	out, err := reg.Run(ctx, "workflow_advance", map[string]any{}, toolContext("coordinator", "sess-1", projectDir))
	testutil.FailErr(t, "reg.Run failed", err)
	var result workflowphases.AdvanceToolResult
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		testutil.FailErr(t, "unmarshal JSON document", err)
	}
	if result.Error != "advance_not_coordinator_mode" {
		t.Fatalf("error = %q want advance_not_coordinator_mode", result.Error)
	}
}

func TestWorkflowAdvanceCoordinatorOnly(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	reg := registerAdvanceToolTestRegistry(t, mgr)
	_, err := reg.Run(context.Background(), "workflow_advance", map[string]any{}, toolContext("implementer", "sess-1", t.TempDir()))
	if err == nil || !strings.Contains(err.Error(), "coordinator") {
		t.Fatalf("err = %v", err)
	}
}

func TestWorkflowAdvanceRejectsArguments(t *testing.T) {
	mgr, _, _, projectDir := testManagerWithRegistry(t)
	reg := registerAdvanceToolTestRegistry(t, mgr)
	ctx := context.Background()
	if _, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0"); err != nil {
		testutil.FailErr(t, "startRun failed", err)
	}
	_, err := reg.Run(ctx, "workflow_advance", map[string]any{"force": true}, toolContext("coordinator", "sess-1", projectDir))
	if err == nil || !strings.Contains(err.Error(), "no arguments") {
		t.Fatalf("err = %v", err)
	}
}

func TestRegisterAdvanceToolRequiresDeps(t *testing.T) {
	if err := workflowphases.RegisterAdvanceTool(nil, &workflowphases.Service{}); err == nil {
		t.Fatal("expected error for nil registry")
	}
	if err := workflowphases.RegisterAdvanceTool(tools.NewDefaultRegistry(), nil); err == nil {
		t.Fatal("expected error for nil run manager")
	}
}
