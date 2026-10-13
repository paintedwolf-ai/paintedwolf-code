package compaction

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tokenest"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCompactionRetainsAndBudgetsReasoning(t *testing.T) {
	trace := &api.ModelReasoning{ProviderID: "p", Model: "m", Text: strings.Repeat("reasoning ", 200), Details: []json.RawMessage{json.RawMessage(`{"type":"thinking","signature":"opaque"}`)}}
	original := []api.Message{
		{ID: "old", Role: api.MessageRoleAssistant, Content: strings.Repeat("earlier ", 1000)},
		{ID: "call", Role: api.MessageRoleAssistant, Content: "read", ModelReasoning: trace, ToolCalls: []api.ToolCall{{ID: "call-1", Name: "read"}}},
		{ID: "result", Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "call-1", Tool: "read", Content: "source"}},
	}
	rows := ContextMessagesFromAPI(original)
	without := append([]ContextMessage(nil), rows...)
	without[1].ModelReasoning = nil
	want := tokenest.EstimateDefault(trace.Text) + tokenest.EstimateDefault(string(trace.Details[0]))
	if EstimateMessagesTokens(rows)-EstimateMessagesTokens(without) != want {
		t.Fatal("reasoning escaped host estimate")
	}
	before, err := compactionMeasure(rows, tokenest.Counter{})
	testutil.FailErr(t, "measure retained reasoning", err)
	after, err := compactionMeasure(without, tokenest.Counter{})
	testutil.FailErr(t, "measure without reasoning", err)
	if before-after != want {
		t.Fatalf("reasoning measurement delta=%d want=%d", before-after, want)
	}
	cfg := testCompactionConfig()
	cfg.KeepRecentMessages = 1
	fitted := DeterministicFit(cfg, rows, EstimateMessagesTokens(rows[1:])+100)
	out := ContextMessagesToAPI(fitted, original)
	var call *api.Message
	for i := range out {
		if out[i].ID == "call" {
			call = &out[i]
		}
	}
	if call == nil || call.ModelReasoning == nil || call.ModelReasoning.Text != trace.Text || string(call.ModelReasoning.Details[0]) != string(trace.Details[0]) || len(call.ToolCalls) != 1 {
		t.Fatal("fit broke retained tool continuation")
	}
	round := ContextMessagesToAPI(rows, original)
	if round[1].ModelReasoning != trace {
		t.Fatal("unchanged context round trip lost replay state")
	}
	if len(FindOversizedChunks(rows, CompactionConfig{ChunkTokenThreshold: 1, KeepRecentMessages: 1}, nil)) != 1 {
		t.Fatal("reasoning-bearing assistant was eligible for chunk rewriting")
	}
}

func TestCompactionBudgetsOpaqueToolContinuationMetadata(t *testing.T) {
	messages := []ContextMessage{{Role: "assistant", ToolCalls: []api.ToolCall{{ID: "call", Name: "read", ExtraContent: map[string]any{"google": map[string]any{"thought_signature": strings.Repeat("opaque", 100)}}}}}}
	baseline := []ContextMessage{{Role: "assistant", ToolCalls: []api.ToolCall{{ID: "call", Name: "read"}}}}
	raw, err := json.Marshal(messages[0].ToolCalls[0].ExtraContent)
	testutil.FailErr(t, "encode continuation metadata", err)
	want := tokenest.EstimateDefault(string(raw))
	if EstimateMessagesTokens(messages)-EstimateMessagesTokens(baseline) != want {
		t.Fatal("opaque continuation escaped context estimate")
	}
	before, err := compactionMeasure(messages, tokenest.Counter{})
	testutil.FailErr(t, "measure metadata", err)
	after, err := compactionMeasure(baseline, tokenest.Counter{})
	testutil.FailErr(t, "measure baseline", err)
	if before-after != want {
		t.Fatal("opaque continuation escaped compaction measurement")
	}
}
