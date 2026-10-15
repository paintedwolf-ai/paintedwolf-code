package modelcall

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNoteCompletionTurnBudget(t *testing.T) {
	t.Cleanup(ResetTurnBudgetForTest)
	for _, tc := range []struct {
		name                string
		cap, total, visible int
		strict              bool
	}{
		{"empty at limit", 16384, 16384, 0, true},
		{"mostly reasoning", 32768, 27000, 1000, true},
		{"normal reasoning", 24576, 9000, 1000, false},
		{"visible output", 16384, 16384, 16384, false},
		{"unknown cap", 0, 16384, 0, false},
		{"unknown usage", 16384, 0, 0, false},
	} {
		NoteCompletionTurnBudget(tc.name, tc.cap, tc.total, tc.visible)
		if SessionStrictBudget(tc.name) != tc.strict {
			t.Errorf("%s: strict=%v", tc.name, SessionStrictBudget(tc.name))
		}
	}
}

func TestEstimateRequestInputAndCatalogRequestBudget(t *testing.T) {
	budgetTokens := 256
	req := CompletionRequest{
		Model:            "m",
		ThinkingOverride: &ThinkingOverride{BudgetTokens: &budgetTokens},
		Messages: []api.Message{
			{Role: api.MessageRoleUser, Content: "Describe", ArtifactIDs: []string{"img-1", "img-2"}},
			{Role: api.MessageRoleAssistant, ModelReasoning: &api.ModelReasoning{Text: "plan", Details: []json.RawMessage{json.RawMessage(`"step"`)}}, ToolCalls: []api.ToolCall{{Name: "read"}}},
			{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Visual: &api.ToolVisualResult{Perceive: true}}},
		},
		Tools: []tools.ToolMeta{{Name: "read", Description: "Read file", ArgsSchema: map[string]any{"type": "object"}}},
	}
	cat := NewCatalog("fixture", []modelinfo.Entry{{ID: "m", ContextLength: 16384, MaxTokens: 4096}})
	budget := cat.RequestBudget(t.Context(), req)
	if budget.Window != 16384 || budget.MinimumOutput != 257 || budget.Input < 3*1600 || budget.Headroom != 1024 {
		t.Fatalf("unexpected catalog request budget: %+v", budget)
	}
}

