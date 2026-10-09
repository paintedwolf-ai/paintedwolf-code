package workflow

import (
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
)

func TestPlanStatusVarAutoAdvances(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDepsWithEvidence())
	ctx := workflowCaller(t, mgr)
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	run, err = advancePlanToApprovePhase(ctx, mgr, run)
	testutil.FailErr(t, "advancePlanToApprovePhase", err)
	if run.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve", run.CurrentPhase)
	}
	run, err = mgr.Approvals.SyncHumanApproval(ctx, run.ID, projectDir)
	testutil.FailErr(t, "SyncHumanApproval", err)
	if run.CurrentPhase != "execute" {
		t.Fatalf("phase = %q want execute after human approval", run.CurrentPhase)
	}
}
