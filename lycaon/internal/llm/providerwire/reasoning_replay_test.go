package providerwire

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReasoningReplayScopes(t *testing.T) {
	trace := &api.ModelReasoning{ProviderID: "p", Model: "m", Text: "trace", Details: []json.RawMessage{json.RawMessage(`{"signature":"opaque"}`)}}
	assistant := api.Message{Role: api.MessageRoleAssistant, ModelReasoning: trace}
	tool := assistant
	tool.ToolCalls = []api.ToolCall{{ID: "call", Name: "read"}}
	foreign := tool
	foreign.ModelReasoning = &api.ModelReasoning{ProviderID: "other", Model: "m", Text: "foreign"}
	empty := tool
	empty.ModelReasoning = &api.ModelReasoning{ProviderID: "p", Model: "m"}
	messages := []api.Message{
		{Role: api.MessageRoleUser, Content: "old"}, tool, assistant,
		{Role: api.MessageRoleUser, Content: "new", ModelReasoning: trace},
		tool, foreign, empty, assistant,
		{Role: api.MessageRoleSystem, Kind: api.MessageKindUserContinuation, Origin: api.MessageOriginHost, Visibility: api.MessageVisibilityInternal},
	}
	for _, tc := range []struct {
		name            string
		scope           ReasoningReplayScope
		provider, model string
		kept            map[int]bool
	}{
		{"none", ReasoningReplayNone, "p", "m", nil},
		{"history", ReasoningReplayHistory, "p", "m", map[int]bool{1: true, 2: true, 4: true, 7: true}},
		{"tool loop", ReasoningReplayCurrentToolLoop, "p", "m", map[int]bool{4: true}},
		{"missing provider", ReasoningReplayHistory, "", "m", nil},
		{"missing model", ReasoningReplayHistory, "p", "", nil},
		{"model switch", ReasoningReplayHistory, "p", "other", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := ProjectReasoning(messages, tc.provider, tc.model, tc.scope)
			for i, m := range out {
				if (m.ModelReasoning != nil) != tc.kept[i] {
					t.Fatalf("row %d replay eligibility incorrect", i)
				}
				if m.ModelReasoning != nil && string(m.ModelReasoning.Details[0]) != `{"signature":"opaque"}` {
					t.Fatal("opaque payload changed")
				}
			}
			if messages[3].ModelReasoning != trace || messages[1].ModelReasoning != trace {
				t.Fatal("projection changed durable history")
			}
		})
	}
	if ProjectReasoning(nil, "p", "m", ReasoningReplayHistory) != nil {
		t.Fatal("empty projection allocated history")
	}
}

func TestProjectReasoningContinuationPolicies(t *testing.T) {
	modelinfo.SetThinkingRules([]modelinfo.ThinkingRule{
		{Match: []string{"claude-opus"}, Style: "adaptive", ContinuationPolicy: "require_fields"},
		{Match: []string{"strip-model"}, Style: "effort_levels", ContinuationPolicy: "strip_reasoning"},
		{Match: []string{"loop-model"}, Style: "thinking_type", ContinuationPolicy: "tool_loop_only"},
		{Match: []string{"retain-model"}, Style: "budget_tokens", ContinuationPolicy: "retain_blocks"},
	})
	t.Cleanup(func() { modelinfo.SetThinkingRules(nil) })

	omitted := json.RawMessage(`{"type":"thinking","signature":"sig-1"}`)
	unsigned := json.RawMessage(`{"type":"thinking","thinking":"draft"}`)
	redacted := json.RawMessage(`{"type":"redacted_thinking","data":"enc"}`)
	emptyRedacted := json.RawMessage(`{"type":"redacted_thinking"}`)
	original := []json.RawMessage{omitted, unsigned, redacted, emptyRedacted}
	msg := api.Message{
		Role: api.MessageRoleAssistant,
		ModelReasoning: &api.ModelReasoning{
			ProviderID: "anthropic",
			Model:      "claude-opus-4-7",
			Details:    original,
		},
	}
	projected := ProjectReasoning([]api.Message{msg}, "anthropic", "claude-opus-4-7", ReasoningReplayHistory)
	if projected[0].ModelReasoning == nil || len(projected[0].ModelReasoning.Details) != 2 {
		t.Fatalf("projected details = %+v", projected[0].ModelReasoning)
	}
	if !strings.Contains(string(projected[0].ModelReasoning.Details[0]), `"thinking":""`) || !strings.Contains(string(projected[0].ModelReasoning.Details[0]), `"signature":"sig-1"`) {
		t.Fatalf("normalized thinking block = %s", projected[0].ModelReasoning.Details[0])
	}
	if string(projected[0].ModelReasoning.Details[1]) != string(redacted) {
		t.Fatalf("redacted block = %s", projected[0].ModelReasoning.Details[1])
	}
	if string(msg.ModelReasoning.Details[0]) != string(omitted) || len(msg.ModelReasoning.Details) != 4 {
		t.Fatal("require_fields mutated durable message details")
	}

	stripped := ProjectReasoning([]api.Message{{
		Role:           api.MessageRoleAssistant,
		ModelReasoning: &api.ModelReasoning{ProviderID: "p", Model: "strip-model", Text: "secret"},
	}}, "p", "strip-model", ReasoningReplayHistory)
	if stripped[0].ModelReasoning != nil {
		t.Fatalf("strip_reasoning kept reasoning: %+v", stripped[0].ModelReasoning)
	}

	loopHistory := []api.Message{
		{Role: api.MessageRoleUser, Content: "turn 1"},
		{
			Role:           api.MessageRoleAssistant,
			ToolCalls:      []api.ToolCall{{ID: "c1", Name: "read"}},
			ModelReasoning: &api.ModelReasoning{ProviderID: "p", Model: "loop-model", Text: "old tool thought"},
		},
		{Role: api.MessageRoleUser, Content: "turn 2"},
		{
			Role:           api.MessageRoleAssistant,
			ToolCalls:      []api.ToolCall{{ID: "c2", Name: "read"}},
			ModelReasoning: &api.ModelReasoning{ProviderID: "p", Model: "loop-model", Text: "active tool thought"},
		},
	}
	loopProjected := ProjectReasoning(loopHistory, "p", "loop-model", ReasoningReplayHistory)
	if loopProjected[1].ModelReasoning != nil || loopProjected[3].ModelReasoning == nil {
		t.Fatalf("tool_loop_only projection = %+v / %+v", loopProjected[1].ModelReasoning, loopProjected[3].ModelReasoning)
	}
}
