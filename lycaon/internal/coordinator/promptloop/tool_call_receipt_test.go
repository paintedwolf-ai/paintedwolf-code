package promptloop

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func toolResultRow(assistantID, callID string) api.Message {
	return api.Message{
		Role: api.MessageRoleTool,
		ToolResult: &api.ToolResult{
			AssistantMessageID: assistantID,
			ToolCallID:         callID,
			Outcome:            api.ToolResultOutcomeRejected,
		},
	}
}

func callIDs(calls []api.ToolCall) []string {
	out := make([]string, 0, len(calls))
	for _, c := range calls {
		out = append(out, c.ID)
	}
	return out
}

func TestAnsweredToolCallsDropsCallsThatNeverRan(t *testing.T) {
	calls := []api.ToolCall{{ID: "call-1", Name: "grep"}}
	got := answeredToolCalls([]api.Message{}, "assistant-1", calls)
	if len(got) != 0 {
		t.Fatalf("answeredToolCalls = %v, want none: an unanswered call must not stay on a tools-omitted turn", callIDs(got))
	}
}

// TestAnsweredToolCallsKeepsAnsweredCalls keeps the call a receipt already names.
func TestAnsweredToolCallsKeepsAnsweredCalls(t *testing.T) {
	calls := []api.ToolCall{{ID: "call-1", Name: "grep"}}
	history := []api.Message{toolResultRow("assistant-1", "call-1")}

	got := answeredToolCalls(history, "assistant-1", calls)
	if len(got) != 1 || got[0].ID != "call-1" {
		t.Fatalf("answeredToolCalls = %v, want [call-1]: dropping it strands the receipt", callIDs(got))
	}
}

func TestAnsweredToolCallsKeepsOnlyTheAnsweredOnes(t *testing.T) {
	calls := []api.ToolCall{
		{ID: "call-ran", Name: "grep"},
		{ID: "call-never-ran", Name: "read"},
	}
	history := []api.Message{toolResultRow("assistant-1", "call-ran")}

	got := answeredToolCalls(history, "assistant-1", calls)
	if len(got) != 1 || got[0].ID != "call-ran" {
		t.Fatalf("answeredToolCalls = %v, want [call-ran] only", callIDs(got))
	}
}

// TestAnsweredToolCallsIgnoresOtherRowsReceipts ignores receipts from other rows.
func TestAnsweredToolCallsIgnoresOtherRowsReceipts(t *testing.T) {
	calls := []api.ToolCall{{ID: "call-1", Name: "grep"}}
	history := []api.Message{toolResultRow("assistant-other", "call-1")}

	got := answeredToolCalls(history, "assistant-1", calls)
	if len(got) != 0 {
		t.Fatalf("answeredToolCalls = %v, want none: the receipt names another row", callIDs(got))
	}
}

func TestAnsweredToolCallsWithoutAnAssistantRowKeepsNothing(t *testing.T) {
	calls := []api.ToolCall{{ID: "call-1", Name: "grep"}}
	history := []api.Message{toolResultRow("", "call-1")}

	got := answeredToolCalls(history, "", calls)
	if len(got) != 0 {
		t.Fatalf("answeredToolCalls = %v, want none: no row means no pair to preserve", callIDs(got))
	}
}

// TestAnsweredToolCallsNeverReturnsNil keeps the storage projection total: the
// assistant row's batch is an empty array, never absent.
func TestAnsweredToolCallsNeverReturnsNil(t *testing.T) {
	if got := answeredToolCalls(nil, "assistant-1", nil); got == nil {
		t.Fatal("answeredToolCalls returned nil; the batch must always be an array")
	}
}
