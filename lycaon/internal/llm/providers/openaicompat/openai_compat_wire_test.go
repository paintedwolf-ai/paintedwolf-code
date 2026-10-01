package openaicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func thinkingTypeProfile() providerprofile.Profile {
	profile := providerprofile.OpenAI()
	profile.Thinking = modelinfo.ThinkStyleThinkingType
	return profile
}

func TestEncodeThinkingTypeOrchestration(t *testing.T) {
	provider := New("fireworks", "https://api.fireworks.ai/inference/v1", "key", []modelinfo.Entry{
		{ID: "accounts/fireworks/models/kimi-k2p6", MaxTokens: 32768},
	}).WithProfile(thinkingTypeProfile())

	req := modelcall.CompletionRequest{
		Model: "accounts/fireworks/models/kimi-k2p6",
		Tools: []tools.ToolMeta{{Name: "update_progress"}},
		Debug: modelcall.RequestDebug{Surface: tools.SurfaceImplementDispatch, SessionID: "s1"},
	}
	body, err := encodeChatCompletionRequest(req, provider, true, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var wire map[string]any
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := wire["reasoning_effort"]; ok {
		t.Fatalf("thinking.type must not send reasoning_effort: %s", body)
	}
	thinking, ok := wire["thinking"].(map[string]any)
	if !ok {
		t.Fatalf("missing thinking object: %s", body)
	}
	if thinking["type"] != "enabled" {
		t.Fatalf("thinking.type = %v, want enabled", thinking["type"])
	}
}

func TestEncodeThinkingTypeOpenTurnEnablesReasoning(t *testing.T) {
	provider := New("fireworks", "https://api.fireworks.ai/inference/v1", "key", []modelinfo.Entry{
		{ID: "accounts/fireworks/models/kimi-k2p6"},
	}).WithProfile(thinkingTypeProfile())

	req := modelcall.CompletionRequest{
		Model:    "accounts/fireworks/models/kimi-k2p6",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
	}
	body, err := encodeChatCompletionRequest(req, provider, false, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(body), `"enabled"`) {
		t.Fatalf("open turn should enable thinking: %s", body)
	}
}

func TestEncodeThinkingTypeAlwaysOnSkipsDisabled(t *testing.T) {
	provider := New("fireworks", "https://api.fireworks.ai/inference/v1", "key", []modelinfo.Entry{
		{ID: "accounts/fireworks/models/kimi-k2p7-code", ThinkingAlwaysOn: true, MaxTokens: 32768},
	}).WithProfile(thinkingTypeProfile())

	req := modelcall.CompletionRequest{
		Think: modelcall.ThinkOff,
		Model: "accounts/fireworks/models/kimi-k2p7-code",
		Tools: []tools.ToolMeta{{Name: "task"}},
		Debug: modelcall.RequestDebug{Surface: tools.SurfaceImplementDispatch},
	}
	body, err := encodeChatCompletionRequest(req, provider, true, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(string(body), `"thinking"`) {
		t.Fatalf("always-on model must omit thinking on orchestration: %s", body)
	}
}

func TestEncodeThinkingTypeOmitsTemperatureWhenThinkingEnabled(t *testing.T) {
	temp := 0.6
	provider := New("fireworks", "https://api.fireworks.ai/inference/v1", "key", []modelinfo.Entry{
		{ID: "accounts/fireworks/models/kimi-k2p6", Temperature: &temp, ReasoningEffort: "high"},
	}).WithProfile(thinkingTypeProfile())

	req := modelcall.CompletionRequest{
		Model: "accounts/fireworks/models/kimi-k2p6",
	}
	body, err := encodeChatCompletionRequest(req, provider, false, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(string(body), `"temperature"`) {
		t.Fatalf("must omit temperature when thinking enabled: %s", body)
	}
}

func TestEncodeThinkingTypeHighOverride(t *testing.T) {
	provider := New("fireworks", "https://api.fireworks.ai/inference/v1", "key", []modelinfo.Entry{
		{ID: "accounts/fireworks/models/kimi-k2p6", ReasoningEffort: "high"},
	}).WithProfile(thinkingTypeProfile())

	req := modelcall.CompletionRequest{
		Model:    "accounts/fireworks/models/kimi-k2p6",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hello"}},
	}
	body, err := encodeChatCompletionRequest(req, provider, false, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(body), `"type":"enabled"`) && !strings.Contains(string(body), `"type": "enabled"`) {
		t.Fatalf("high override should enable thinking: %s", body)
	}
}

func TestEncodeThinkingTypeAllowsTemperatureWhenThinkingDisabled(t *testing.T) {
	temp := 0.6
	provider := New("fireworks", "https://api.fireworks.ai/inference/v1", "key", []modelinfo.Entry{
		{ID: "accounts/fireworks/models/kimi-k2p6", Temperature: &temp, MaxTokens: 32768},
	}).WithProfile(thinkingTypeProfile())

	req := modelcall.CompletionRequest{
		Think: modelcall.ThinkOff,
		Model: "accounts/fireworks/models/kimi-k2p6",
		Tools: []tools.ToolMeta{{Name: "task"}},
		Debug: modelcall.RequestDebug{Surface: tools.SurfaceImplementDispatch},
	}
	body, err := encodeChatCompletionRequest(req, provider, true, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(body), `"temperature":0.6`) && !strings.Contains(string(body), `"temperature": 0.6`) {
		t.Fatalf("orchestration with thinking disabled should keep temperature: %s", body)
	}
}

func TestEncodeOmitsResponseFormatWhenNil(t *testing.T) {
	provider := New("openai", "https://api.openai.com/v1", "key", []modelinfo.Entry{
		{ID: "gpt-4.1-mini"},
	})
	req := modelcall.CompletionRequest{
		Model:    "gpt-4.1-mini",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
	}
	body, err := encodeChatCompletionRequest(req, provider, false, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(string(body), "response_format") {
		t.Fatalf("nil ResponseFormat must omit field: %s", body)
	}
}

func TestEncodePreservesLiteralHTMLInToolWire(t *testing.T) {
	provider := New("openai", "https://api.openai.com/v1", "key", []modelinfo.Entry{
		{ID: "gpt-4.1-mini"},
	})
	snippet := `fn f() -> Vec<u8> { "a & b" }`
	req := modelcall.CompletionRequest{
		Model: "gpt-4.1-mini",
		Messages: []api.Message{
			{
				Role: api.MessageRoleAssistant,
				ToolCalls: []api.ToolCall{{
					ID:   "call_1",
					Name: "write",
					Args: map[string]any{"path": "main.rs", "content": snippet},
				}},
			},
			{
				Role:    api.MessageRoleTool,
				Content: `{"content":"1\t` + snippet + `"}`,
				ToolResult: &api.ToolResult{
					ToolCallID: "call_1",
					Content:    `{"content":"1\t` + snippet + `"}`,
				},
			},
		},
	}
	body, err := encodeChatCompletionRequest(req, provider, false, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	out := string(body)
	for _, bad := range []string{`\u003c`, `\u003e`, `\u0026`} {
		if strings.Contains(out, bad) {
			t.Fatalf("LLM wire HTML-escaped %q (breaks write/edit copy-paste): %s", bad, out)
		}
	}
	for _, want := range []string{"Vec<u8>", "a & b"} {
		if !strings.Contains(out, want) {
			t.Fatalf("want literal %q in wire body: %s", want, out)
		}
	}
}

func TestMapChatCompletionCapturesReasoningField(t *testing.T) {
	var response chatCompletionResponseWire
	testutil.FailErr(t, "decode response", json.Unmarshal([]byte(`{
		"choices":[{"message":{"content":"answer","reasoning":"vendor trace"},"finish_reason":"stop"}]
	}`), &response))
	completion := mapChatCompletionWire(&response)
	if completion == nil {
		t.Fatal("mapped completion is nil")
	}
	if completion.Content != "answer" || completion.Reasoning != "vendor trace" {
		t.Fatalf("completion = %+v", completion)
	}
}

func TestReasoningWireTextSupportsVendorResponseFields(t *testing.T) {
	if got := reasoningWireText("reasoning-field", "reasoning-content-field"); got != "reasoning-field" {
		t.Fatalf("reasoning field = %q", got)
	}
	if got := reasoningWireText("", "reasoning-content-field"); got != "reasoning-content-field" {
		t.Fatalf("reasoning_content field = %q", got)
	}
}
