package promptloop

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/compaction"
	"github.com/lycaon/lycaon/pkg/api"
)

// Compaction fitting rebuilds every request; the assembly breakpoint must
// survive it so providers receive an explicit marker.
func TestFitMessagesForCompactionKeepsPromptCacheBreakpoint(t *testing.T) {
	loop := NewPromptLoop(PromptLoopDeps{
		Model: ModelDeps{
			CompactionConfig: func(context.Context, *api.Session) compaction.CompactionConfig {
				return compaction.CompactionConfig{Enabled: true, HardCeilingTokens: 1 << 20, KeepRecentMessages: 16}
			},
		},
	})
	msgs := []api.Message{
		{Role: api.MessageRoleSystem, Content: "stable", ContextPinned: true},
		{ID: "u1", Role: api.MessageRoleUser, Content: "go"},
		{ID: "a1", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "c1", Name: "read", Args: map[string]any{"path": "a"}}}},
		{ID: "t1", Role: api.MessageRoleTool, Content: "result", PromptCacheBreakpoint: api.PromptCacheTierHistory,
			ToolResult: &api.ToolResult{ToolCallID: "c1", Tool: "read", Content: "result"}},
		{Role: api.MessageRoleSystem, Content: "volatile"},
	}
	out := loop.Model.fitMessagesForCompaction(context.Background(), &api.Session{ID: "s"}, "s", msgs, nil)
	var marked []int
	for i, m := range out {
		if m.PromptCacheBreakpoint != api.PromptCacheTierNone {
			marked = append(marked, i)
		}
	}
	if len(marked) != 1 || marked[0] != 3 || out[3].PromptCacheBreakpoint != api.PromptCacheTierHistory {
		t.Fatalf("breakpoints after fit = %v, want the history tier on [3] (tool result)", marked)
	}
}
