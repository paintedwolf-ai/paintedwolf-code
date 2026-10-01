//go:build integration

package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAdvanceCompoundGateUnmetListsLeaves(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	setTestRegistry(t, mgr, blueprintMgr, conditions.TestRegistryDeps())
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	run, err = completePlanResearchAtDepthNone(ctx, mgr, run)
	testutil.FailErr(t, "completePlanResearchAtDepthNone", err)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	run, err = advancePlanToApprovePhase(ctx, mgr, run)
	testutil.FailErr(t, "advancePlanToApprovePhase", err)
	run, err = mgr.SyncHumanApproval(workflowCaller(t, mgr), run.ID, projectDir)
	testutil.FailErr(t, "SyncHumanApproval", err)
	if run.CurrentPhase != "execute" {
		t.Fatalf("phase = %q want execute", run.CurrentPhase)
	}
	before, err := mgr.Get(ctx, run.ID)
	testutil.FailErr(t, "Get paused parent", err)
	auto, err := mgr.TryAutoAdvance(ctx, run.ID)
	testutil.FailErr(t, "TryAutoAdvance paused parent", err)
	if auto.Status != api.WorkflowRunStatusPausedOnChild || auto.Revision != before.Revision {
		t.Fatalf("auto-advance mutated paused parent: %+v", auto)
	}
	_, err = mgr.Pause(ctx, run.ID, "hold")
	var notRunnable *NotRunnableError
	if !errors.As(err, &notRunnable) || notRunnable.Status != api.WorkflowRunStatusPausedOnChild {
		t.Fatalf("Pause parent err = %v, want paused_on_child not runnable", err)
	}
	_, err = mgr.Advance(ctx, run.ID)
	if !errors.As(err, &notRunnable) || notRunnable.Status != api.WorkflowRunStatusPausedOnChild {
		t.Fatalf("Advance parent err = %v, want paused_on_child not runnable", err)
	}
}
