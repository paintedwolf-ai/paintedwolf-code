package llm

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerretry"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestReasoningWireCatalogInheritanceAndOverride(t *testing.T) {
	ship := map[string]ProviderEntry{"custom": {ID: "custom", ReasoningWire: providerprofile.ReasoningWireContent}}
	for _, style := range []providerprofile.ReasoningWireStyle{"", providerprofile.ReasoningWireNone, providerprofile.ReasoningWireContent, providerprofile.ReasoningWireDetails} {
		entry, err := resolveLocalEntry(ProviderEntry{ID: "endpoint", Kind: "custom", ReasoningWire: style}, ship)
		testutil.FailErr(t, "resolve reasoning wire", err)
		want := style
		if want == "" {
			want = providerprofile.ReasoningWireContent
		}
		if entry.ReasoningWire != want || entry.LocalReasoningWire() != style {
			t.Fatalf("style %q: effective=%q local=%q", style, entry.ReasoningWire, entry.LocalReasoningWire())
		}
	}
	if _, err := decodeProviderConfig([]byte("providers:\n  - id: custom\n    reasoning_wire: unsupported\n")); err == nil {
		t.Fatal("unknown reasoning format was accepted")
	}
}

func TestShippedReasoningWireDefaults(t *testing.T) {
	cfg, err := LoadProviderConfig()
	testutil.FailErr(t, "load provider catalog", err)
	wants := map[string]providerprofile.ReasoningWireStyle{
		"cloudflare-workers-ai": providerprofile.ReasoningWireContent,
		"lmstudio":              providerprofile.ReasoningWireContent,
		"omlx":                  providerprofile.ReasoningWireContent,
		"openai-compatible":     providerprofile.ReasoningWireContent,
		"openrouter":            providerprofile.ReasoningWireDetails,
	}
	for _, entry := range cfg.Providers {
		if want, ok := wants[entry.ID]; ok {
			if entry.ReasoningWire != want {
				t.Fatalf("%s reasoning_wire=%q, want %q", entry.ID, entry.ReasoningWire, want)
			}
			delete(wants, entry.ID)
		}
	}
	if len(wants) != 0 {
		t.Fatalf("missing provider defaults: %v", wants)
	}
}

func TestReasoningWireConfiguresCompatibleProviderKinds(t *testing.T) {
	for _, kind := range []string{"custom", "openai-compatible", "lmstudio", "omlx", "openrouter", "cloudflare-workers-ai"} {
		provider, err := newProviderForEntry(t.Context(), CatalogEntry{
			ID: "instance", Kind: kind, BaseURL: "https://example.test/v1", ReasoningWire: providerprofile.ReasoningWireContent,
		}, "fixture", providerretry.ProviderHTTPRetry{}, nil, nil)
		testutil.FailErr(t, "construct "+kind, err)
		if provider.Profile().ReasoningWire != providerprofile.ReasoningWireContent {
			t.Fatalf("%s did not apply reasoning_wire", kind)
		}
	}
	for _, profile := range []providerprofile.Profile{
		providerprofile.LocalInference(providerprofile.OpenAI()),
		providerprofile.Lmstudio(),
		providerprofile.Omlx(),
		providerprofile.Litellm(),
	} {
		if profile.ReasoningWire != providerprofile.ReasoningWireContent {
			t.Fatalf("local profile reasoning_wire=%q, want %q", profile.ReasoningWire, providerprofile.ReasoningWireContent)
		}
	}
}

func TestLocalProviderKindsReplayReasoningContentAcrossMultiStepToolLoop(t *testing.T) {
	withThinkingRules(t, nil)
	cfg, err := LoadProviderConfig()
	testutil.FailErr(t, "load provider catalog", err)
	templates, err := assembleProviderTemplates(cfg.Providers)
	testutil.FailErr(t, "assemble provider templates", err)
	ship := preferredProviderTemplates(templates)
	for _, kind := range []string{"openai-compatible", "lmstudio", "omlx"} {
		entry, err := resolveLocalEntry(ProviderEntry{
			ID: "local-endpoint", Kind: kind, BaseURL: "https://example.test/v1",
			Models: []modelinfo.Entry{{ID: "local-4b-instruct"}},
		}, ship)
		testutil.FailErr(t, "resolve "+kind, err)
		provider, err := newProviderForEntry(t.Context(), entry, "", providerretry.ProviderHTTPRetry{}, nil, nil)
		testutil.FailErr(t, "construct "+kind, err)
		p := provider.(*openaicompat.Provider)
		req := modelcall.CompletionRequest{
			Model: "local-4b-instruct",
			Tools: []tools.ToolMeta{{Name: "update_progress"}, {Name: "request_tools"}, {Name: "mkdir"}},
			Messages: []api.Message{
				{Role: api.MessageRoleSystem, Content: "Coordinator instructions."},
				{Role: api.MessageRoleUser, Content: "Build me a tic tac toe game with AI I can beat"},
				{
					Role:           api.MessageRoleAssistant,
					ToolCalls:      []api.ToolCall{{ID: "call_1", WireID: "wire_1", Name: "update_progress", Args: map[string]any{"content": "- [ ] Plan game"}}},
					ModelReasoning: &api.ModelReasoning{ProviderID: entry.ID, Model: "local-4b-instruct", Text: "Plan the deliverables before requesting filesystem tools."},
				},
				{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "call_1", Content: "ok"}},
				{
					Role:           api.MessageRoleAssistant,
					ToolCalls:      []api.ToolCall{{ID: "call_2", WireID: "wire_2", Name: "request_tools", Args: map[string]any{"need": "mkdir"}}},
					ModelReasoning: &api.ModelReasoning{ProviderID: entry.ID, Model: "local-4b-instruct", Text: "Load mkdir to create the game directory."},
				},
				{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "call_2", Content: "loaded mkdir"}},
			},
		}
		wire := encodeWire(t, p, req)
		data, err := json.Marshal(wire["messages"])
		testutil.FailErr(t, "marshal wire messages for "+kind, err)
		var msgs []openaicompat.Message
		testutil.FailErr(t, "unmarshal wire messages for "+kind, json.Unmarshal(data, &msgs))
		if len(msgs) != 6 {
			t.Fatalf("%s message count=%d, want 6", kind, len(msgs))
		}
		if msgs[2].ReasoningContent != "Plan the deliverables before requesting filesystem tools." {
			t.Fatalf("%s step 1 reasoning_content=%q", kind, msgs[2].ReasoningContent)
		}
		if msgs[4].ReasoningContent != "Load mkdir to create the game directory." {
			t.Fatalf("%s step 2 reasoning_content=%q", kind, msgs[4].ReasoningContent)
		}
	}
}

func TestReasoningContentStreamSurvivesNextRequest(t *testing.T) {
	withThinkingRules(t, nil)
	var sent map[string]any
	srv := streamReasoningServer(t, []string{
		`{"choices":[{"delta":{"reasoning_content":"Inspect the manifest\n"}}]}`,
		`{"choices":[{"delta":{"reasoning_content":"before choosing a repair."}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"wire-call","type":"function","function":{"name":"read","arguments":"{\"path\":\"manifest.json\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":30}}`,
	}, &sent)
	defer srv.Close()
	// Custom instances use the same catalog resolution as bundled kinds.
	entry, err := resolveLocalEntry(ProviderEntry{ID: "custom", ReasoningWire: providerprofile.ReasoningWireContent}, nil)
	testutil.FailErr(t, "resolve custom provider", err)
	entry.BaseURL, entry.Models = srv.URL, []modelinfo.Entry{{ID: "model"}}
	provider, err := newProviderForEntry(t.Context(), entry, "key", (&Registry{}).providerRetryPolicy(entry), nil, nil)
	testutil.FailErr(t, "construct configured transport", err)
	p := provider.(*openaicompat.Provider)
	req := modelcall.CompletionRequest{Model: "model", Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Inspect the release."}}, Tools: []tools.ToolMeta{{Name: "read"}}}
	ch, err := p.Stream(t.Context(), req)
	testutil.FailErr(t, "start fixture stream", err)
	completion, _, err := modelcall.CollectStream(ch)
	testutil.FailErr(t, "collect fixture stream", err)
	if completion.Reasoning != "Inspect the manifest\nbefore choosing a repair." || len(completion.ToolCalls) != 1 {
		t.Fatalf("lost reasoning or tool call: %+v", completion)
	}
	completion.ProviderID, completion.Model = entry.ID, req.Model
	assistant := api.Message{Role: api.MessageRoleAssistant, ToolCalls: completion.ToolCalls, ModelReasoning: completion.ModelReasoning()}
	req.Messages = append(req.Messages, assistant, api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: completion.ToolCalls[0].ID, Content: "manifest data"}})
	ch, err = p.Stream(t.Context(), req)
	testutil.FailErr(t, "start continuation", err)
	_, _, err = modelcall.CollectStream(ch)
	testutil.FailErr(t, "collect continuation", err)
	msgs := sent["messages"].([]any)
	m := msgs[1].(map[string]any)
	if m["reasoning_content"] != completion.Reasoning || m["reasoning"] != nil || m["reasoning_details"] != nil {
		t.Fatalf("wrong reasoning wire fields: %v", m)
	}
	if msgs[2].(map[string]any)["tool_call_id"] != m["tool_calls"].([]any)[0].(map[string]any)["id"] {
		t.Fatal("continuation lost tool-call pairing")
	}
	for _, provenance := range [][2]string{{"other", "model"}, {"custom", "other"}, {"", ""}} {
		assistant.ModelReasoning.ProviderID, assistant.ModelReasoning.Model = provenance[0], provenance[1]
		req.Messages = []api.Message{assistant}
		wire := encodeWire(t, p, req)
		data, err := json.Marshal(wire["messages"])
		testutil.FailErr(t, "encode foreign history", err)
		var decoded []openaicompat.Message
		testutil.FailErr(t, "decode foreign history", json.Unmarshal(data, &decoded))
		if decoded[0].ReasoningContent != "" {
			t.Fatal("reasoning crossed provider/model provenance")
		}
	}
}
