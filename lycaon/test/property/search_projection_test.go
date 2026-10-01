package property

import (
	"testing"

	"github.com/lycaon/lycaon/internal/search"
	"github.com/lycaon/lycaon/pkg/api"
	"pgregory.net/rapid"
)

func TestProjectToolMessageNonEmptyForRejectedOutcomes(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		code := rapid.String().Draw(t, "code")
		msg := api.Message{
			ID:      rapid.String().Draw(t, "id"),
			Role:    api.MessageRoleTool,
			Content: "Rejected: " + code,
			ToolResult: &api.ToolResult{
				Outcome:    api.ToolResultOutcomeRejected,
				ToolCallID: rapid.String().Draw(t, "call"),
				Content:    "Rejected: " + code,
			},
		}
		rows := search.ProjectToolMessage("proj", "sess", msg, "read")
		if len(rows) == 0 {
			t.Fatal("rejected tool result must project evidence")
		}
	})
}

func TestProjectLifecycleEvidenceNonEmptyForWorkflowFeedback(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		answer := rapid.StringMatching(`[a-zA-Z0-9][a-zA-Z0-9 ]{0,80}`).Draw(t, "answer")
		msg := api.Message{
			ID:   rapid.String().Draw(t, "id"),
			Role: api.MessageRoleSystem,
			Kind: api.MessageKindWorkflowFeedback,
			WorkflowFeedback: &api.WorkflowFeedbackMeta{
				Answer: answer,
			},
			Content: answer,
		}
		rows := search.ProjectLifecycleEvidence("proj", "sess", msg)
		if len(rows) == 0 {
			t.Fatal("workflow_feedback answer must project evidence")
		}
	})
}
