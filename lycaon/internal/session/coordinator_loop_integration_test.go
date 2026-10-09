//go:build integration

package session_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/coordinator/surface"
	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
)

// Pending human approval suppresses the coordinator wake.
func TestLoopSkippedInApprovePhase(t *testing.T) {
	fix := setupLoopFixture(t, settings.DefaultSessionLimits())
	ctx := context.Background()
	run, err := fix.wfMgr.Store.Runs.ActiveBySession(ctx, fix.sess.ID)
	if err != nil || run == nil {
		t.Fatal("missing run")
	}
	run.CurrentPhase = "approve"
	if err := fix.wfMgr.Store.Update(ctx, run); err != nil {
		testutil.FailErr(t, "fix.wfMgr.Store.Update failed", err)
	}
	vars, err := fix.wfMgr.Store.Runs.GetScaffoldVars(ctx, run.ID)
	testutil.FailErr(t, "GetScaffoldVars", err)
	vars = runstate.StampHumanApprovalPhase(vars, &workflowdef.HumanApprovalConfig{}, run.BlueprintPath)
	vars = runstate.SetHumanApprovalReady(vars, true)
	testutil.FailErr(t, "UpdateVars", fix.wfMgr.Store.UpdateVars(ctx, run, t.TempDir(), vars))
	before, _ := fix.store.GetMessages(ctx, fix.sess.ID)
	fix.mgr.NudgeCoordinatorLoop(ctx, fix.sess.ID, anchor.LegFinished, anchor.LegFinished, "leg-1", anchor.Envelope{})
	fix.mgr.WaitForCoordinatorAsyncTurns(testutil.BoundedContext(t, 5*time.Second))
	after, err := fix.store.GetMessages(ctx, fix.sess.ID)
	testutil.FailErr(t, "fix.store.GetMessages failed", err)
	if len(after) != len(before) {
		t.Fatalf("expected no auto-prompt in approve; before=%d after=%d", len(before), len(after))
	}
}

func TestLoopNoKickPlusSentinel(t *testing.T) {
	fix := setupLoopFixture(t, settings.DefaultSessionLimits())
	ctx := context.Background()
	fix.mgr.NudgeCoordinatorLoop(ctx, fix.sess.ID, anchor.LegFinished, anchor.LegFinished, "leg-1", anchor.Envelope{})
	fix.mgr.WaitForCoordinatorAsyncTurns(testutil.BoundedContext(t, 5*time.Second))
	msgs, err := fix.store.GetMessages(ctx, fix.sess.ID)
	testutil.FailErr(t, "fix.store.GetMessages failed", err)
	for _, msg := range msgs {
		if msg.Role != api.MessageRoleUser {
			continue
		}
		if strings.Contains(msg.Content, surface.HostLoopWakeSentinel) && strings.Contains(strings.ToLower(msg.Content), "leg finished") {
			t.Fatalf("loop-wake sentinel turn must not also include leg-finished kick: %q", msg.Content)
		}
	}
}
