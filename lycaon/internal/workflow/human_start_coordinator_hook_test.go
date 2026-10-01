package workflow

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestHumanWorkflowStartInvokesPhaseAutoAdvancedHook(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()

	var hooked bool
	var prevPhase, newPhase string
	mgr.OnPhaseAutoAdvanced = func(_ context.Context, sid, _ string, previous, next string) {
		if sid != sessionID {
			t.Fatalf("session_id = %q", sid)
		}
		prevPhase, newPhase = previous, next
		hooked = true
	}

	_, err := mgr.StartHuman(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID:      "plan",
		WorkflowVersion: "1.0.0",
		Request:         "test request",
	})
	testutil.FailErr(t, "mgr.StartHuman failed", err)
	if !hooked {
		t.Fatal("expected OnPhaseAutoAdvanced on human workflow start")
	}
	if prevPhase != "" || newPhase != "research" {
		t.Fatalf("phases %q -> %q, want \"\" -> research", prevPhase, newPhase)
	}
}

func TestRejectedCoordinatorWorkflowStartDoesNotInvokePhaseAutoAdvancedHook(t *testing.T) {
	mgr, sessionID, _ := testWorkflowManager(t)
	ctx := context.Background()

	hooked := false
	mgr.OnPhaseAutoAdvanced = func(context.Context, string, string, string, string) {
		hooked = true
	}

	_, err := mgr.Start(ctx, sessionID, api.StartWorkflowRunRequest{
		WorkflowID:      "plan",
		WorkflowVersion: "1.0.0",
	})
	if !errors.Is(err, ErrWorkflowStartRequiresHumanApproval) {
		t.Fatalf("Start error = %v, want ErrWorkflowStartRequiresHumanApproval", err)
	}
	if hooked {
		t.Fatal("rejected coordinator start should not schedule phase-advanced loop wake")
	}
}
