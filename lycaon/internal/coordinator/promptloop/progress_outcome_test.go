package promptloop

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestChecklistNoOpAndRelabelDoNotAdvanceBatch(t *testing.T) {
	for _, body := range []string{`{"status":"unchanged","state_changed":false}`, `{"status":"updated","document_changed":true,"state_changed":false}`} {
		history := []api.Message{{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{AssistantMessageID: "assistant", Tool: "update_progress", Content: body, Outcome: api.ToolResultOutcomeCompleted}}}
		if batchMadeProgress(history, "assistant") {
			t.Fatalf("bookkeeping advanced batch: %s", body)
		}
		history[0].ToolResult.Content = `{"status":"updated","state_changed":true,"reconciled":true}`
		if !batchMadeProgress(history, "assistant") {
			t.Fatal("deliverable transition did not advance batch")
		}
		history = append(history, api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{AssistantMessageID: "assistant", Tool: "command", Outcome: api.ToolResultOutcomeCompleted}})
		if !batchMadeProgress(history, "assistant") {
			t.Fatal("checklist prevented independent work")
		}
	}
}
