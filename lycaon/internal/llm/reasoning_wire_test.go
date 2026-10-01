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
	wants := map[string]providerprofile.ReasoningWireStyle{"cloudflare-workers-ai": providerprofile.ReasoningWireContent, "openrouter": providerprofile.ReasoningWireDetails}
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
	for _, kind := range []string{"custom", "openrouter", "cloudflare-workers-ai"} {
		provider, err := newProviderForEntry(t.Context(), CatalogEntry{
			ID: "instance", Kind: kind, BaseURL: "https://example.test/v1", ReasoningWire: providerprofile.ReasoningWireContent,
		}, "fixture", providerretry.ProviderHTTPRetry{}, nil, nil)
		testutil.FailErr(t, "construct "+kind, err)
		if provider.Profile().ReasoningWire != providerprofile.ReasoningWireContent {
			t.Fatalf("%s did not apply reasoning_wire", kind)
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
