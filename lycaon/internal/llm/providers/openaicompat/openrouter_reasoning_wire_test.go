package openaicompat

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

const openrouterKimi = "moonshotai/kimi-k2.7-code"

func openrouterProvider(t *testing.T, entries ...modelinfo.Entry) *Provider {
	t.Helper()
	if len(entries) == 0 {
		entries = []modelinfo.Entry{{ID: openrouterKimi}}
	}
	return New("openrouter-1", "https://openrouter.ai/api/v1", "key", entries).
		WithProfile(providerprofile.Openrouter()).WithReasoningWire(providerprofile.ReasoningWireDetails)
}

func encodeWire(t *testing.T, p *Provider, req modelcall.CompletionRequest) map[string]any {
	t.Helper()
	body, err := encodeChatCompletionRequest(req, p, true, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return wire
}

// Model thinking rules resolve to the transport’s supported wire format.
func TestOpenRouterFoldsFamilyStyleOntoReasoningObject(t *testing.T) {
	withThinkingRules(t, []modelinfo.ThinkingRule{
		{Match: []string{"kimi-k2p7-code", "kimi-k2.7-code"}, Style: string(modelinfo.ThinkStyleThinkingType)},
	})
	p := openrouterProvider(t)

	if style := p.modelThinking(openrouterKimi).Style; style != modelinfo.ThinkStyleReasoningObject {
		t.Fatalf("resolved style = %q want %q", style, modelinfo.ThinkStyleReasoningObject)
	}

	wire := encodeWire(t, p, modelcall.CompletionRequest{
		Model: openrouterKimi,
		Tools: []tools.ToolMeta{{Name: "write"}},
		Debug: modelcall.RequestDebug{Surface: tools.SurfaceImplementInvestigate, SessionID: "s1"},
	})

	if _, ok := wire["thinking"]; ok {
		t.Fatalf("thinking must not reach OpenRouter: %v", wire)
	}
	reasoning, ok := wire["reasoning"].(map[string]any)
	if !ok {
		t.Fatalf("missing reasoning object: %v", wire)
	}
	if reasoning["effort"] != "medium" {
		t.Fatalf("reasoning = %v want effort:medium on an orchestration turn", reasoning)
	}
}

// The transport accepts exactly one reasoning control format.
func TestOpenRouterNeverSendsBothReasoningControls(t *testing.T) {
	for _, rule := range []string{
		string(modelinfo.ThinkStyleThinkingType),
		string(modelinfo.ThinkStyleEffortLevels),
		string(modelinfo.ThinkStyleBudgetTokens),
		string(modelinfo.ThinkStyleBooleanThink),
		string(modelinfo.ThinkStyleReasoningObject),
	} {
		t.Run(rule, func(t *testing.T) {
			withThinkingRules(t, []modelinfo.ThinkingRule{{Match: []string{"kimi"}, Style: rule}})
			p := openrouterProvider(t)
			for _, level := range []modelcall.ThinkLevel{modelcall.ThinkUnset, modelcall.ThinkOff, modelcall.ThinkLow, modelcall.ThinkMedium, modelcall.ThinkHigh} {
				wire := encodeWire(t, p, modelcall.CompletionRequest{
					Model: openrouterKimi,
					Think: level,
					Tools: []tools.ToolMeta{{Name: "write"}},
					Debug: modelcall.RequestDebug{Surface: tools.SurfaceImplementInvestigate},
				})
				_, hasObject := wire["reasoning"]
				_, hasEffort := wire["reasoning_effort"]
				_, hasThinking := wire["thinking"]
				if hasEffort || hasThinking {
					t.Fatalf("level %v put a non-gateway control on the wire: %v", level, wire)
				}
				if hasObject && hasEffort {
					t.Fatalf("level %v sent both reasoning forms: %v", level, wire)
				}
			}
		})
	}
}

// Routing excludes endpoints that omit requested parameters.
func TestOpenRouterPinsRoutingToCapableEndpoints(t *testing.T) {
	withThinkingRules(t, nil)
	wire := encodeWire(t, openrouterProvider(t), modelcall.CompletionRequest{
		Model: openrouterKimi,
		Tools: []tools.ToolMeta{{Name: "write"}},
		Debug: modelcall.RequestDebug{Surface: tools.SurfaceImplementInvestigate},
	})
	provider, ok := wire["provider"].(map[string]any)
	if !ok {
		t.Fatalf("missing provider routing block: %v", wire)
	}
	if provider["require_parameters"] != true {
		t.Fatalf("provider = %v want require_parameters:true", provider)
	}
}

// Direct endpoints omit routing controls.
func TestDirectHostSendsNoProviderRoutingBlock(t *testing.T) {
	withThinkingRules(t, nil)
	p := New("fireworks", "https://api.fireworks.ai/inference/v1", "key",
		[]modelinfo.Entry{{ID: "accounts/fireworks/models/kimi-k2p7-code"}})
	wire := encodeWire(t, p, modelcall.CompletionRequest{
		Model: "accounts/fireworks/models/kimi-k2p7-code",
		Tools: []tools.ToolMeta{{Name: "write"}},
		Debug: modelcall.RequestDebug{Surface: tools.SurfaceImplementInvestigate},
	})
	if _, ok := wire["provider"]; ok {
		t.Fatalf("direct host must not send provider routing: %v", wire)
	}
}

func reasoningMessage(t *testing.T, providerID, model string) api.Message {
	t.Helper()
	return api.Message{
		Role:    api.MessageRoleAssistant,
		Content: "checking",
		ToolCalls: []api.ToolCall{
			{ID: "call_1", Name: "read", Args: map[string]any{"path": "a.go"}},
		},
		ModelReasoning: &api.ModelReasoning{
			ProviderID: providerID,
			Model:      model,
			Text:       "need the file first",
			Details: []json.RawMessage{
				json.RawMessage(`{"type":"reasoning.text","text":"need the file first","index":0}`),
			},
		},
	}
}

func assistantWire(t *testing.T, p *Provider, msgs []api.Message) Message {
	t.Helper()
	body, err := encodeChatCompletionRequest(modelcall.CompletionRequest{Model: openrouterKimi, Messages: msgs}, p, true, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var wire struct {
		Messages []Message `json:"messages"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, m := range wire.Messages {
		if m.Role == "assistant" {
			return m
		}
	}
	t.Fatal("no assistant message on the wire")
	return Message{}
}

// Tool continuations retain the original reasoning blocks.
func TestReasoningReplaysToItsOwnProviderAndModel(t *testing.T) {
	withThinkingRules(t, nil)
	got := assistantWire(t, openrouterProvider(t),
		[]api.Message{reasoningMessage(t, "openrouter-1", openrouterKimi)})

	if got.Reasoning != "need the file first" {
		t.Fatalf("reasoning = %q want the recorded trace", got.Reasoning)
	}
	if len(got.ReasoningDetails) != 1 {
		t.Fatalf("reasoning_details = %v want one block", got.ReasoningDetails)
	}
	if string(got.ReasoningDetails[0]) != `{"type":"reasoning.text","text":"need the file first","index":0}` {
		t.Fatalf("reasoning_details rewritten: %s", got.ReasoningDetails[0])
	}
}

// Signed reasoning belongs to its originating provider and model.
func TestReasoningDoesNotReplayAcrossProviderOrModel(t *testing.T) {
	withThinkingRules(t, nil)
	for name, msg := range map[string]api.Message{
		"other provider": reasoningMessage(t, "openrouter-2", openrouterKimi),
		"other model":    reasoningMessage(t, "openrouter-1", "moonshotai/kimi-k2.6"),
		"no provenance":  reasoningMessage(t, "", ""),
	} {
		t.Run(name, func(t *testing.T) {
			got := assistantWire(t, openrouterProvider(t), []api.Message{msg})
			if got.Reasoning != "" || len(got.ReasoningDetails) != 0 {
				t.Fatalf("replayed a foreign trace: %+v", got)
			}
		})
	}
}

// Replay is opt-in because endpoints can reject unknown fields.
func TestReasoningDoesNotReplayOnDriversThatDidNotOptIn(t *testing.T) {
	withThinkingRules(t, nil)
	p := New("openrouter-1", "https://openrouter.ai/api/v1", "key",
		[]modelinfo.Entry{{ID: openrouterKimi}})
	got := assistantWire(t, p, []api.Message{reasoningMessage(t, "openrouter-1", openrouterKimi)})
	if got.Reasoning != "" || len(got.ReasoningDetails) != 0 {
		t.Fatalf("driver without reasoning replay echoed reasoning: %+v", got)
	}
}
