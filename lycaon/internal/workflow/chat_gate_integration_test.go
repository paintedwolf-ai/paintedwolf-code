//go:build integration

package workflow

import (
	"context"
	"github.com/lycaon/lycaon/internal/conditions"
	"github.com/lycaon/lycaon/internal/testutil"
	"testing"
)

func TestTryResolveUserFeedbackDoesNotApprovePlanViaChat(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := context.Background()

	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)

	run, err = mgr.Phases.TryAutoAdvanceThroughCommittedGates(ctx, run.ID, 8)
	testutil.FailErr(t, "auto-advance intake", err)
	if run.CurrentPhase != "research" {
		t.Fatalf("phase = %q want research after intake", run.CurrentPhase)
	}

	for _, msg := range []string{"LGTM — approved", "go", "confirm", "approved"} {
		if err := mgr.Feedback.TryResolveUserFeedback(ctx, "sess-1", "", testutil.HostOwner().ID, msg); err != nil {
			testutil.FailErr(t, "TryResolveUserFeedback", err)
		}
		vars, err := mgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
		testutil.FailErr(t, "GetScaffoldVars", err)
		if conditions.DotPathEquals(vars, "plan.status", "approved") {
			t.Fatalf("chat %q must not satisfy human_approval", msg)
		}
		run, err = mgr.Store.Runs.Get(ctx, run.ID)
		testutil.FailErr(t, "Get", err)
		if run.CurrentPhase != "research" {
			t.Fatalf("phase = %q want still research after chat %q", run.CurrentPhase, msg)
		}
	}
}

// Approval advances only through a host action.
func TestHumanApprovalChatPhraseDoesNotAdvanceAtApprovePhase(t *testing.T) {
	mgr, _, blueprintMgr, _ := testManagerWithRegistry(t)
	ctx := context.Background()

	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	run, err = advancePlanToApprovePhase(ctx, mgr, run)
	testutil.FailErr(t, "advancePlanToApprovePhase", err)
	if run.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve", run.CurrentPhase)
	}

	for _, msg := range []string{"go", "approved", "lgtm", "yes let's proceed"} {
		if err := mgr.Feedback.TryResolveUserFeedback(ctx, "sess-1", "", testutil.HostOwner().ID, msg); err != nil {
			testutil.FailErr(t, "TryResolveUserFeedback", err)
		}
		run, err = mgr.Store.Runs.Get(ctx, run.ID)
		testutil.FailErr(t, "Get", err)
		if run.CurrentPhase != "approve" {
			t.Fatalf("phase = %q want still approve after chat %q (host action approves, not prose)", run.CurrentPhase, msg)
		}
	}
}

func TestSyncHumanApprovalAutoAdvancesPlanRun(t *testing.T) {
	mgr, _, blueprintMgr, projectDir := testManagerWithRegistry(t)
	ctx := context.Background()

	run, err := startRun(ctx, mgr, "sess-1", "plan", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	run = completePlanIntakeT(ctx, t, mgr, run)
	seedValidPlanContent(t, blueprintMgr, run.BlueprintPath)
	run, err = advancePlanToApprovePhase(ctx, mgr, run)
	testutil.FailErr(t, "advancePlanToApprovePhase", err)
	if run.CurrentPhase != "approve" {
		t.Fatalf("phase = %q want approve", run.CurrentPhase)
	}

	synced, err := mgr.Approvals.SyncHumanApproval(workflowCaller(t, mgr), run.ID, projectDir)
	testutil.FailErr(t, "SyncHumanApproval", err)
	if synced == nil || synced.CurrentPhase != "execute" {
		t.Fatalf("phase = %q want execute after API approval", synced.CurrentPhase)
	}
}
