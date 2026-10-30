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

func TestProgressClassificationPreservesDeclaredResultFraming(t *testing.T) {
	for _, body := range []string{`{"status":"unchanged","state_changed":false}`, `{"status":"updated","state_changed":true}`, `{"status":"unchanged","reconciliation_status":"acknowledged"}`} {
		for _, decoration := range []struct{ prefix, suffix string }{{"", ""}, {"[progress#1]\n", "\n\n>>> Tool feedback\nRecorded diagnostic"}} {
			plain := []api.Message{{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{AssistantMessageID: "assistant", Tool: "update_progress", Content: body, Outcome: api.ToolResultOutcomeCompleted}}}
			expected := classifyBatchProgress(plain, "assistant")
			plain[0].ToolResult.Content = decoration.prefix + body + decoration.suffix
			if actual := classifyBatchProgress(plain, "assistant"); actual != expected {
				t.Fatalf("declared framing changed outcome: plain=%+v decorated=%+v", expected, actual)
			}
			if progressResultAdvanced(decoration.prefix+body+decoration.suffix) != expected.Advanced {
				t.Fatal("raw and transcript progress outcomes diverged")
			}
		}
	}
}
