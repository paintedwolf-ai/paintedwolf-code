package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestExecuteToolCallsInTurnCommitsAssistantBeforeFirstTool(t *testing.T) {
	var commitBeforeAppend bool
	reg := tools.NewStubRegistry()
	_ = reg.Register("answer_decision", func(_ context.Context, _ map[string]any, _ tools.ToolContext) (string, error) {
		return `{"status":"resumed"}`, nil
	})

	loop := NewPromptLoopForTest(PromptLoopDeps{
		Context: ContextDeps{
			Tools: reg,
		},
	})
	sess := &api.Session{ID: "sess-1", Posture: api.SessionPostureBuild}
	assistantID := "asst-1"
	history := []api.Message{{
		ID:         assistantID,
		Role:       api.MessageRoleAssistant,
		Visibility: api.MessageVisibilityInternal,
		Content:    "Two workers need decisions.",
		ToolCalls: []api.ToolCall{
			{ID: "tc1", Name: "answer_decision", Args: map[string]any{"job_id": "job-1", "option": "1"}},
		},
	}}

	loop.Projection.Deps.UpdateMessage = func(_ context.Context, _, _ string, msg api.Message) error {
		if msg.Visibility == api.MessageVisibilityTranscript {
			commitBeforeAppend = true
		}
		for i := range history {
			if history[i].ID == msg.ID {
				history[i] = msg
			}
		}
		return nil
	}
	var appended []api.Message
	loop.Projection.Deps.AppendMessages = func(_ context.Context, _ string, msgs ...api.Message) error {
		if !commitBeforeAppend {
			t.Fatal("assistant must commit to transcript before tool result append")
		}
		appended = append(appended, msgs...)
		return nil
	}

	_, turnTools, _, _, _, _, err := loop.Batch.executeToolCallsInTurn(
		context.Background(),
		sess,
		sess.ID,
		history[0].ToolCalls,
		tools.ToolContext{
			Identity: tools.InvocationIdentity{SessionID: sess.ID},
		},
		history,
		"dispatch",
		assistantID,
		"",
		nil,
	)
	testutil.FailErr(t, "executeToolCallsInTurn", err)
	if len(turnTools) != 1 || turnTools[0] != "answer_decision" {
		t.Fatalf("turnTools = %v want [answer_decision]", turnTools)
	}
	if len(appended) != 1 || appended[0].Role != api.MessageRoleTool {
		t.Fatalf("appended = %+v want one tool row", appended)
	}
	if history[0].Visibility != api.MessageVisibilityTranscript {
		t.Fatalf("assistant visibility = %q want transcript", history[0].Visibility)
	}
}
