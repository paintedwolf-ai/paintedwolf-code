//go:build integration

package inputs_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/orchestration"
	"github.com/lycaon/lycaon/internal/session"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// answeredAskBody returns the resolved tool row.
func answeredAskBody(t *testing.T, fx *askUserFixture, ctx context.Context, toolCallID string) map[string]any {
	t.Helper()
	msgs, err := fx.wfMgr.Policy.Sessions.(session.Store).GetMessages(ctx, fx.sess.ID)
	testutil.FailErr(t, "GetMessages", err)
	for _, m := range msgs {
		if m.Role != wire.MessageRoleTool || m.ToolResult == nil {
			continue
		}
		if m.ToolResult.ToolCallID != toolCallID {
			continue
		}
		var got map[string]any
		testutil.FailErr(t, "unmarshal answer", json.Unmarshal([]byte(m.Content), &got))
		return got
	}
	t.Fatalf("ask_user tool row %q not rewritten", toolCallID)
	return nil
}

// openChoiceAsk adds the original tool row for result projection.
func openChoiceAsk(t *testing.T, fx *askUserFixture, ctx context.Context, toolCallID string) string {
	t.Helper()
	out, err := fx.runAskUser(ctx, map[string]any{
		"prompt":        "Which capability first?",
		"response_type": "single_choice",
		"options":       []any{"IOC reputation", "Log triage"},
	}, tools.ToolContext{
		Identity: tools.InvocationIdentity{SessionID: fx.sess.ID,
			Agent:      orchestration.ProfileCoordinator,
			ToolCallID: toolCallID},
	})
	testutil.FailErr(t, "ask_user", err)
	var body map[string]any
	testutil.FailErr(t, "unmarshal", json.Unmarshal([]byte(out), &body))
	testutil.FailErr(t, "AppendMessages", fx.wfMgr.Policy.Sessions.(session.Store).AppendMessages(ctx, fx.sess.ID, wire.Message{
		ID:      "msg-" + toolCallID,
		Role:    wire.MessageRoleTool,
		Content: out,
		ToolResult: &wire.ToolResult{
			Tool:       "ask_user",
			ToolCallID: toolCallID,
			Content:    out,
		},
	}))
	phaseID, _ := body["phase_id"].(string)
	return phaseID
}

func TestAskUserMarksOffMenuAnswers(t *testing.T) {
	t.Run("picked option is not off menu", func(t *testing.T) {
		fx := setupAskUserIntegration(t)
		ctx := context.Background()
		run, err := fx.wfMgr.Starts.StartHuman(ctx, fx.sess.ID, wire.StartWorkflowRunRequest{
			WorkflowID: "ask-user-int", WorkflowVersion: "1.0.0",
		})
		testutil.FailErr(t, "StartHuman", err)
		phaseID := openChoiceAsk(t, fx, ctx, "call_on_menu")

		_, err = fx.wfMgr.Feedback.ResolveUserDecision(fx.ctx, fx.sess.ID, run.ID, phaseID, []string{"Log triage"}, "")
		testutil.FailErr(t, "ResolveUserDecision", err)

		got := answeredAskBody(t, fx, ctx, "call_on_menu")
		if off, ok := got["off_menu"]; ok && off != false {
			t.Fatalf("off_menu = %v, want absent for a listed option", off)
		}
	})

	t.Run("answer past the options is off menu", func(t *testing.T) {
		fx := setupAskUserIntegration(t)
		ctx := context.Background()
		run, err := fx.wfMgr.Starts.StartHuman(ctx, fx.sess.ID, wire.StartWorkflowRunRequest{
			WorkflowID: "ask-user-int", WorkflowVersion: "1.0.0",
		})
		testutil.FailErr(t, "StartHuman", err)
		phaseID := openChoiceAsk(t, fx, ctx, "call_off_menu")

		_, err = fx.wfMgr.Feedback.ResolveUserDecision(fx.ctx, fx.sess.ID, run.ID, phaseID, []string{"All of the above"}, "")
		testutil.FailErr(t, "ResolveUserDecision", err)

		got := answeredAskBody(t, fx, ctx, "call_off_menu")
		if got["off_menu"] != true {
			t.Fatalf("off_menu = %v, want true for an answer outside the options", got["off_menu"])
		}
		if got["response"] != "All of the above" {
			t.Fatalf("response = %v", got["response"])
		}
	})
}
