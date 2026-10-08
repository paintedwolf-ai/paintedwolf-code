package promptloop

import (
	"context"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
	"testing"
	"time"
)

func TestReviewResultRetainsOfferedPhaseForReplay(t *testing.T) {
	st := &promptLoopTurnState{}
	st.coordinatorFrame.RunContext.RunID = "run"
	st.coordinatorFrame.RunContext.CurrentPhase = "claims"
	original := &api.ToolResult{Tool: "submit_verdict", Feedback: []api.ToolFeedback{{Code: "TOOL_ARGS_INVALID"}}}
	msg := api.Message{ToolResult: original}
	bindReviewResult(&msg, st)
	if msg.WorkflowRunID != "run" || msg.ToolResult.Feedback[0].Details["workflow_phase"] != "claims" {
		t.Fatalf("missing review binding: %+v", msg)
	}
	if original.Feedback[0].Details != nil {
		t.Fatal("binding mutated the invocation's feedback")
	}
}

func TestReviewRepairObservesDurableRejectedResult(t *testing.T) {
	var persisted, observed bool
	loop := NewPromptLoopForTest(PromptLoopDeps{
		AppendMessages: func(_ context.Context, _ string, rows ...api.Message) error { persisted = true; return nil },
		RecordReviewToolResult: func(_ context.Context, _ string, msg api.Message) error {
			if !persisted {
				t.Fatal("repair accounting ran before transcript commit")
			}
			if msg.WorkflowRunID != "run" || msg.ToolResult.Feedback[0].Details["workflow_phase"] != "claims" {
				t.Fatal("repair accounting lost offered workflow binding")
			}
			observed = true
			return nil
		},
	})
	st := &promptLoopTurnState{}
	st.coordinatorFrame.RunContext.RunID = "run"
	st.coordinatorFrame.RunContext.CurrentPhase = "claims"
	msg := api.Message{ID: "result", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "submit_verdict", ToolCallID: "call", AssistantMessageID: "response", Outcome: api.ToolResultOutcomeRejected, Feedback: []api.ToolFeedback{{Code: "TOOL_ARGS_INVALID"}}}}
	var last time.Time
	_, err := toolBatch{loop}.persistClassifiedToolOutcome(t.Context(), "session", &api.Session{ID: "session"}, nil, toolCallOutcome{toolMsg: msg, toolName: "submit_verdict"}, &last, st)
	testutil.FailErr(t, "persist rejected verdict", err)
	if !observed {
		t.Fatal("durable rejection did not reach review accounting")
	}
}
