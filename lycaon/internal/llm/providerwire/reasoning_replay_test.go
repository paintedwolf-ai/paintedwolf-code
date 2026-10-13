package providerwire

import (
	"encoding/json"
	"testing"

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
