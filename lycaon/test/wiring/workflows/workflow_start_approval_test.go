package workflows

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/test/wiring"

	"github.com/lycaon/lycaon/internal/testutil"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestStateStartRequiresHumanApproval(t *testing.T) {
	h := wiring.BuildForTest(t)
	dir := t.TempDir()
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{
		Posture: wire.SessionPostureBuild,
	}, dir)
	testutil.FailErr(t, "create session in store", err)
	ctx := context.Background()

	tctx := wiring.WiringToolContext(sess.ID, sess.WorkspacePath)
	tctx.Identity.Agent = "coordinator"
	_, err = h.ToolRegistry.Run(ctx, "state_start", map[string]any{
		"workflow_id": "plan", "workflow_version": "1.0.1",
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
	h := wiring.BuildForTest(t)
	dir := t.TempDir()
	sess, err := h.CreateHarnessSession(t, wire.CreateSessionRequest{
		Posture: wire.SessionPostureBuild,
	}, dir)
	testutil.FailErr(t, "create session in store", err)

	_, handled, err := h.Workflows.Manager.Slash.TrySlashPrompt(context.Background(), sess.ID, "/plan", "")
	testutil.FailErr(t, "h.Workflows.Manager.Slash.TrySlashPrompt failed", err)
	if !handled {
		t.Fatal("expected /plan handled")
	}
	active, err := h.Workflows.Manager.Store.Runs.ActiveBySession(context.Background(), sess.ID)
	testutil.FailErr(t, "h.Workflows.Manager.GetActive failed", err)
	if active.WorkflowID != "plan" {
		t.Fatalf("workflow_id = %q", active.WorkflowID)
	}
}
