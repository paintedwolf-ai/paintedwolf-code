package workflow

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestRegistryGateEvaluatorBlocksPlanStub(t *testing.T) {
	reg, err := conditions.NewDefaultRegistry(conditions.TestRegistryDeps())
	testutil.FailErr(t, "conditions.NewDefaultRegistry failed", err)
	eval := RegistryGateEvaluator{Registry: reg}
	manifest := workflowdef.Manifest{
		PhaseDefs: []workflowdef.PhaseDef{{ID: "stub", CompleteWhen: "plan_stub_valid"}},
	}
	run := &api.WorkflowRun{CurrentPhase: "stub", SessionID: "s1"}
	ok, result, err := eval.PhaseGateMet(context.Background(), manifest, run, nil)
	testutil.FailErr(t, "eval.PhaseGateMet failed", err)
	if ok || result.Reason != "plan_stub_valid" {
		t.Fatalf("ok=%v reason=%q", ok, result.Reason)
	}
}

func TestAdvanceUnregisteredCompleteWhenBlocked(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	reg := conditions.NewRegistry()
	mgr.SetConditionRegistry(reg)
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "unreg",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{
			{ID: "only", CompleteWhen: "totally_unknown_gate_xyz"},
		},
	})
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"unreg@1.0.0": manifest})
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "unreg", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	_, err = mgr.Advance(ctx, run.ID)
	if err == nil {
		t.Fatal("expected phase gate error for unregistered complete_when")
	}
	if _, ok := IsPhaseGateUnmet(err); !ok {
		t.Fatalf("err = %v", err)
	}
}

func TestAdvanceBlockedByRegistryGate(t *testing.T) {
	mgr, _, _, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "complete research at depth none", err)
	_, err = mgr.Advance(ctx, run.ID)
	if err == nil {
		t.Fatal("expected phase gate error without plan stub")
	}
	if _, ok := IsPhaseGateUnmet(err); !ok {
		t.Fatalf("err = %v", err)
	}
}

func TestAdvanceAfterPlanStub(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "complete research at depth none", err)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	run, err = mgr.Advance(ctx, run.ID)
	if err != nil {
		t.Fatalf("advance: %v", err)
	}
	if run.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve", run.CurrentPhase)
	}
}

func TestAdvanceImplementPhaseCompletesRun(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDepsWithEvidence())
	ctx := workflowCaller(t, mgr)

	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	run, err = advancePlanToApprovePhase(ctx, mgr, run)
	testutil.FailErr(t, "advancePlanToApprovePhase", err)
	run, err = mgr.SyncHumanApproval(ctx, run.ID, projectDir)
	testutil.FailErr(t, "SyncHumanApproval", err)
	if run.CurrentPhase != "execute" {
		t.Fatalf("phase = %q want execute", run.CurrentPhase)
	}
	parentID := run.ID
	child, err := mgr.Store.ActiveBySession(ctx, "sess-1")
	testutil.FailErr(t, "ActiveBySession child", err)
	if child == nil || child.ParentRunID == nil {
		t.Fatal("expected active implement child run")
	}
	TerminalChildRunForTest(ctx, t, mgr, child.ID)
	run, err = mgr.Get(ctx, parentID)
	testutil.FailErr(t, "Get parent after child complete", err)
	if run.Status != api.WorkflowRunStatusComplete {
		t.Fatalf("status = %q want complete", run.Status)
	}
}
