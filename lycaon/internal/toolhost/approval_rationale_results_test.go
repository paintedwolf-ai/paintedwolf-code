package toolhost

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestRationaleObservationsStopAtTheHeldAction(t *testing.T) {
	result := func(content string) api.Message {
		return api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "read", Content: content}}
	}
	messages := []api.Message{
		result("previous turn"), {Role: api.MessageRoleUser, Content: "current goal"},
		result("file does not exist"),
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "held"}}},
		result("later outcome"),
		{Role: api.MessageRoleUser, Content: "future goal"},
	}
	held := rationaleMessagesThroughAction(messages, "held")
	if len(held) != 4 || api.UserIntentBoundary(held) != 2 {
		t.Fatalf("held action acquired a future user intent: %+v", held)
	}
	if got := rationaleMessagesThroughAction(messages, "missing"); len(got) != 0 {
		t.Fatal("unknown action acquired unrelated intent")
	}
	got := recentRationaleResults(messages, 2, "held")
	if !strings.Contains(got, "file does not exist") || strings.Contains(got, "previous turn") || strings.Contains(got, "later outcome") {
		t.Fatalf("rationale crossed the held-action boundary: %s", got)
	}
	if got := recentRationaleResults(messages, 2, "missing"); got != "" {
		t.Fatal("unknown action consumed later evidence")
	}
}
