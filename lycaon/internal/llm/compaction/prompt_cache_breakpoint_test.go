package compaction

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

// The compactor IR round trip runs on every turn; it must not strip the marker.
func TestContextMessageRoundTripKeepsPromptCacheBreakpoint(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleSystem, Content: "stable", ContextPinned: true},
		{ID: "result", Role: api.MessageRoleTool, PromptCacheBreakpoint: api.PromptCacheTierHistory,
			ToolResult: &api.ToolResult{ToolCallID: "call", Tool: "read", Content: "body"}},
		{Role: api.MessageRoleSystem, Content: "volatile"},
	}
	out := ContextMessagesToAPI(ContextMessagesFromAPI(msgs), msgs)
	if len(out) != len(msgs) {
		t.Fatalf("rows = %d, want %d", len(out), len(msgs))
	}
	for i, m := range out {
		if m.PromptCacheBreakpoint != msgs[i].PromptCacheBreakpoint {
			t.Fatalf("row %d PromptCacheBreakpoint = %v, want %v", i, m.PromptCacheBreakpoint, msgs[i].PromptCacheBreakpoint)
		}
	}
}

// The marked row sits in the retained tail, so trimming keeps it marked.
func TestDeterministicFitKeepsPromptCacheBreakpointOnRetainedTail(t *testing.T) {
	msgs := withCharter(fitRows(300))
	msgs[len(msgs)-1].PromptCacheBreakpoint = api.PromptCacheTierHistory
	out := DeterministicFit(fitCfg(), msgs, EstimateMessagesTokens(msgs)/4)
	if len(out) >= len(msgs) {
		t.Fatalf("expected a trim, kept %d of %d rows", len(out), len(msgs))
	}
	if out[len(out)-1].PromptCacheBreakpoint != api.PromptCacheTierHistory {
		t.Fatal("trim dropped the prompt cache breakpoint from the retained tail")
	}
}
