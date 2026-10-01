package workflow

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

func TestWorkflowUserFeedbackPending(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	reg := tools.NewDefaultRegistry()
	if err := RegisterFeedbackTool(reg, mgr); err != nil {
		testutil.FailErr(t, "RegisterFeedbackTool failed", err)
	}
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "feedback-tool",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID: "clarify",
			OnEnter: workflowdef.PhaseOnEnter{
				RequestUserFeedback: &workflowdef.UserFeedbackPrompt{Prompt: "REST or GraphQL?"},
			},
		}},
	})
	mgr.Manifests = workflowdef.NewRegistry(map[string]workflowdef.Manifest{"feedback-tool@1.0.0": manifest})
	ctx := context.Background()
	run, err := startRun(ctx, mgr, "sess-1", "feedback-tool", "1.0.0")
	testutil.FailErr(t, "startRun failed", err)
	_ = run
	out, err := reg.Run(ctx, "workflow_user_feedback", map[string]any{}, tools.ToolContext{
		Agent: "coordinator", SessionID: "sess-1",
	})
	testutil.FailErr(t, "reg.Run failed", err)
	if !strings.Contains(out, `"pending":true`) || !strings.Contains(out, `"phase_id":"clarify"`) {
		t.Fatalf("out = %q", out)
	}
	if !strings.Contains(out, "REST or GraphQL?") {
		t.Fatalf("prompt missing: %q", out)
	}
}

func TestWorkflowUserFeedbackNotPending(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	reg := tools.NewDefaultRegistry()
	if err := RegisterFeedbackTool(reg, mgr); err != nil {
		testutil.FailErr(t, "RegisterFeedbackTool failed", err)
	}
	out, err := reg.Run(context.Background(), "workflow_user_feedback", map[string]any{}, tools.ToolContext{
		Agent: "coordinator", SessionID: "sess-no-run",
	})
	testutil.FailErr(t, "reg.Run failed", err)
	if !strings.Contains(out, `"pending":false`) {
		t.Fatalf("out = %q", out)
	}
}

func TestWorkflowUserFeedbackCoordinatorOnly(t *testing.T) {
	mgr, _, _, _ := testManager(t)
	reg := tools.NewDefaultRegistry()
	if err := RegisterFeedbackTool(reg, mgr); err != nil {
		testutil.FailErr(t, "RegisterFeedbackTool failed", err)
	}
	_, err := reg.Run(context.Background(), "workflow_user_feedback", map[string]any{}, tools.ToolContext{
		Agent: "implementer", SessionID: "sess-1",
	})
	if err == nil || !strings.Contains(err.Error(), "coordinator") {
		t.Fatalf("err = %v", err)
	}
}
