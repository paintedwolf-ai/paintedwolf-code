package wiring

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/toolrejection"
	wire "github.com/lycaon/lycaon/pkg/api"
	"testing"
)

func TestStateStartRequiresHumanApproval(t *testing.T) {
	h := BuildForTest(t)
	dir := t.TempDir()
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{
		Posture: wire.SessionPostureBuild,
	}, dir)
	testutil.FailErr(t, "create session in store", err)
	ctx := context.Background()

	tctx := wiringToolContext(sess.ID, sess.WorkspacePath)
	tctx.Identity.Agent = "coordinator"
	_, err = h.ToolRegistry.Run(ctx, "state_start", map[string]any{
		"workflow_id": "plan", "workflow_version": "1.0.0",
	}, tctx)
	var reject *toolrejection.ToolReject
	if !errors.As(err, &reject) || reject.Code != "WORKFLOW_START_REQUIRES_HUMAN_APPROVAL" {
		t.Fatalf("state_start err = %v, want WORKFLOW_START_REQUIRES_HUMAN_APPROVAL reject", err)
	}
	if got, _ := reject.Data["workflow_id"].(string); got != "plan" {
		t.Fatalf("reject workflow_id = %q, want plan", got)
	}
}

func TestStateStartAfterSlash(t *testing.T) {
	h := BuildForTest(t)
	dir := t.TempDir()
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{
		Posture: wire.SessionPostureBuild,
	}, dir)
	testutil.FailErr(t, "create session in store", err)

	_, handled, err := h.WorkflowMgr.Slash.TrySlashPrompt(context.Background(), sess.ID, "/plan", "")
	testutil.FailErr(t, "h.WorkflowMgr.Slash.TrySlashPrompt failed", err)
	if !handled {
		t.Fatal("expected /plan handled")
	}
	active, err := h.WorkflowMgr.Store.Runs.ActiveBySession(context.Background(), sess.ID)
	testutil.FailErr(t, "h.WorkflowMgr.GetActive failed", err)
	if active.WorkflowID != "plan" {
		t.Fatalf("workflow_id = %q", active.WorkflowID)
	}
}
