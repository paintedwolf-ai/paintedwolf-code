package promptloop

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestChecklistNoOpAndRelabelDoNotAdvanceBatch(t *testing.T) {
	for _, body := range []string{`{"status":"unchanged","state_changed":false}`, `{"status":"updated","document_changed":true,"state_changed":false}`} {
		history := []api.Message{{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{AssistantMessageID: "assistant", Tool: "update_progress", Content: body, Outcome: api.ToolResultOutcomeCompleted}}}
		if result := classifyBatchProgress(history, "assistant"); result.Advanced || result.Blocked {
			t.Fatalf("bookkeeping advanced batch: %s", body)
		}
		history[0].ToolResult.Content = `{"status":"updated","state_changed":true,"reconciled":true}`
		if !classifyBatchProgress(history, "assistant").Advanced {
			t.Fatal("deliverable transition did not advance batch")
		}
		history = append(history, api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{AssistantMessageID: "assistant", Tool: "command", Outcome: api.ToolResultOutcomeCompleted}})
		if !classifyBatchProgress(history, "assistant").Advanced {
			t.Fatal("checklist prevented independent work")
		}
	}
}

func TestNeutralChecklistCannotHideARejectedOperation(t *testing.T) {
	history := []api.Message{
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{AssistantMessageID: "assistant", Tool: "update_progress", Content: `{"status":"unchanged"}`, Outcome: api.ToolResultOutcomeCompleted}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{AssistantMessageID: "assistant", Tool: "edit", Outcome: api.ToolResultOutcomeRejected}},
	}
	result := classifyBatchProgress(history, "assistant")
	if result.Advanced || !result.Blocked {
		t.Fatalf("no-op masked rejected work: %+v", result)
	}
}
