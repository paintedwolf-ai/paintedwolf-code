package security

import (
	"context"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestCoordinatorToolReturnsManifestPrompt(t *testing.T) {
	manifest := workflowdef.FinalizeManifest(workflowdef.Manifest{
		ID:      "feedback-tool-e2e",
		Version: "1.0.0",
		PhaseDefs: []workflowdef.PhaseDef{{
			ID: "clarify",
			OnEnter: workflowdef.PhaseOnEnter{
				RequestUserFeedback: &workflowdef.UserFeedbackPrompt{Prompt: "Which database engine?"},
			},
		}},
	})
	h, sess := buildWorkflowHarnessWithManifest(t, manifest)
	toolReg := h.ToolRegistry
	workflowMgr := h.WorkflowMgr
	ctx := context.Background()
	if _, err := workflowMgr.StartHuman(ctx, sess.ID, wire.StartWorkflowRunRequest{
		WorkflowID: "feedback-tool-e2e", WorkflowVersion: "1.0.0",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := toolReg.Run(ctx, "workflow_user_feedback", map[string]any{}, tools.ToolContext{
		Agent: "coordinator", SessionID: sess.ID,
	})
	testutil.FailErr(t, "toolReg.Run failed", err)
	if !strings.Contains(out, "Which database engine?") || !strings.Contains(out, `"phase_id":"clarify"`) {
		t.Fatalf("out = %q", out)
	}
}
