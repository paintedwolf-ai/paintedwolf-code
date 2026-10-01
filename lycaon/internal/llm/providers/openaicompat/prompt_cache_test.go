package openaicompat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestEncodeOpenAINoPromptCacheOmitsPromptCacheKey(t *testing.T) {
	provider := New("together", "https://api.together.xyz/v1", "key", []modelinfo.Entry{
		{ID: "meta-llama/Llama-3.3-70B-Instruct-Turbo"},
	}).WithProfile(providerprofile.Together()).WithPromptCache(shippedPromptCache(t, "together"))
	req := modelcall.CompletionRequest{
		Model:    "meta-llama/Llama-3.3-70B-Instruct-Turbo",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		Debug:    modelcall.RequestDebug{SessionID: "sess-1"},
	}
	body, err := encodeChatCompletionRequest(req, provider, false, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(string(body), "prompt_cache_key") {
		t.Fatalf("an implicit cache takes no prompt_cache_key: %s", body)
	}
}

func TestEncodeFireworksPromptCacheKeyAndAffinityHeader(t *testing.T) {
	provider := New("fireworks", "https://api.fireworks.ai/inference/v1", "key", []modelinfo.Entry{
		{ID: "accounts/fireworks/models/llama-v3p1-70b-instruct"},
	}).WithProfile(providerprofile.Fireworks()).WithPromptCache(shippedPromptCache(t, "fireworks"))
	req := modelcall.CompletionRequest{
		Model:    "accounts/fireworks/models/llama-v3p1-70b-instruct",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		Debug:    modelcall.RequestDebug{SessionID: "sess-fw"},
	}
	body, err := encodeChatCompletionRequest(req, provider, false, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(body), `"prompt_cache_key":"sess-fw"`) && !strings.Contains(string(body), `"prompt_cache_key": "sess-fw"`) {
		t.Fatalf("fireworks should send prompt_cache_key: %s", body)
	}
	headers := provider.promptCacheRequestHeaders(req)
	if headers["x-session-affinity"] != "sess-fw" {
		t.Fatalf("x-session-affinity = %q, want sess-fw", headers["x-session-affinity"])
	}
	if strings.Contains(string(body), "prompt_cache_breakpoint") {
		t.Fatalf("fireworks must not send OpenAI breakpoints: %s", body)
	}
}

func TestEncodeOpenAIGPT56PromptCacheBreakpoint(t *testing.T) {
	provider := New("openai", "https://api.openai.com/v1", "key", []modelinfo.Entry{
		{ID: "gpt-5.6"},
	}).WithProfile(providerprofile.OpenAI()).WithPromptCache(shippedPromptCache(t, "openai"))
	req := modelcall.CompletionRequest{
		Model: "gpt-5.6",
		Messages: []api.Message{
			{Role: api.MessageRoleSystem, Content: "stable system", PromptCacheBreakpoint: api.PromptCacheTierHistory},
			{Role: api.MessageRoleUser, Content: "go"},
		},
		Debug: modelcall.RequestDebug{SessionID: "sess-oai"},
	}
	body, err := encodeChatCompletionRequest(req, provider, false, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, `"prompt_cache_key":"sess-oai"`) && !strings.Contains(s, `"prompt_cache_key": "sess-oai"`) {
		t.Fatalf("missing prompt_cache_key: %s", s)
	}
	if !strings.Contains(s, `"prompt_cache_breakpoint"`) || !strings.Contains(s, `"mode":"explicit"`) {
		t.Fatalf("gpt-5.6 should send prompt_cache_breakpoint: %s", s)
	}
	if strings.Contains(s, `"prompt_cache_retention"`) {
		t.Fatalf("gpt-5.6 must not send deprecated prompt_cache_retention: %s", s)
	}
	if strings.Contains(s, `"prompt_cache_options"`) {
		t.Fatalf("keep default implicit mode; must not send prompt_cache_options: %s", s)
	}
}

// The coordinator marks the last history message, usually a tool result; the
// marker lands on that message's text part and nowhere else.
func TestEncodeOpenAIGPT56BreakpointOnToolResult(t *testing.T) {
	provider := New("openai", "https://api.openai.com/v1", "key", []modelinfo.Entry{
		{ID: "gpt-5.6"},
	}).WithProfile(providerprofile.OpenAI()).WithPromptCache(shippedPromptCache(t, "openai"))
	req := modelcall.CompletionRequest{
		Model: "gpt-5.6",
		Messages: []api.Message{
			{Role: api.MessageRoleSystem, Content: "stable system"},
			{Role: api.MessageRoleUser, Content: "go"},
			{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "call-1", Name: "read", Args: map[string]any{"path": "a"}}}},
			{Role: api.MessageRoleTool, Content: "result", ToolResult: &api.ToolResult{ToolCallID: "call-1", Tool: "read"}, PromptCacheBreakpoint: api.PromptCacheTierHistory},
			{Role: api.MessageRoleSystem, Content: "volatile board"},
		},
		Debug: modelcall.RequestDebug{SessionID: "sess-oai"},
	}
	body, err := encodeChatCompletionRequest(req, provider, false, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	var wire struct {
		Messages []struct {
			Role       string          `json:"role"`
			Content    json.RawMessage `json:"content"`
			ToolCallID string          `json:"tool_call_id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(body, &wire); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if strings.Count(string(body), `"prompt_cache_breakpoint"`) != 1 {
		t.Fatalf("want exactly one breakpoint: %s", body)
	}
	tool := wire.Messages[3]
	if tool.Role != "tool" || tool.ToolCallID != "call-1" {
		t.Fatalf("message 3 = %+v, want the tool result", tool)
	}
	var parts []ContentPart
	if err := json.Unmarshal(tool.Content, &parts); err != nil || len(parts) != 1 {
		t.Fatalf("tool content = %s, want one text part", tool.Content)
	}
	if parts[0].Type != "text" || parts[0].Text != "result" || parts[0].PromptCacheBreakpoint == nil || parts[0].PromptCacheBreakpoint.Mode != promptCacheBreakpointExplicit {
		t.Fatalf("tool part = %+v, want text with explicit breakpoint", parts[0])
	}
	for _, i := range []int{0, 1, 4} {
		if !json.Valid(wire.Messages[i].Content) || wire.Messages[i].Content[0] != '"' {
			t.Fatalf("message %d content = %s, want a plain string", i, wire.Messages[i].Content)
		}
	}
}

// breakpointProjection marks every tiered row with OpenAI's breakpoint.
func breakpointProjection(msgs []api.Message) providerwire.PromptCacheProjection {
	proj := providerwire.PromptCacheProjection{Marker: providerprofile.PromptCacheMarkerBreakpoint}
	for i, m := range msgs {
		if m.PromptCacheBreakpoint != api.PromptCacheTierNone {
			proj.Breakpoints = append(proj.Breakpoints, providerwire.PromptCacheBreakpoint{Index: i, Tier: m.PromptCacheBreakpoint})
		}
	}
	return proj
}

// A tool-call-only assistant turn has no part to carry the marker; it lands on
// the message before it rather than vanishing.
func TestProjectMessagesBreakpointFallsBackPastEmptyAssistant(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleSystem, Content: "stable"},
		{Role: api.MessageRoleUser, Content: "go"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "call-1", Name: "read", Args: map[string]any{}}}, PromptCacheBreakpoint: api.PromptCacheTierHistory},
	}
	out := ProjectMessages(msgs, MessageProjection{PromptCache: breakpointProjection(msgs)})
	if len(out) != 3 {
		t.Fatalf("messages = %d, want 3", len(out))
	}
	if _, isParts := out[2].Content.([]ContentPart); isParts {
		t.Fatalf("assistant content = %+v, want no marker on an empty body", out[2].Content)
	}
	parts, ok := out[1].Content.([]ContentPart)
	if !ok || len(parts) != 1 || parts[0].Text != "go" || parts[0].PromptCacheBreakpoint == nil {
		t.Fatalf("user content = %+v, want the breakpoint on its text part", out[1].Content)
	}
	if _, isParts := out[0].Content.([]ContentPart); isParts {
		t.Fatalf("system content = %+v, want untouched", out[0].Content)
	}
}

// Two boundaries on distinct rows mark both; the same row marks once.
func TestProjectMessagesMarksEachBoundaryOnce(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleSystem, Content: "prefix", PromptCacheBreakpoint: api.PromptCacheTierHistory},
		{Role: api.MessageRoleUser, Content: "go"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "c", Name: "read", Args: map[string]any{}}}},
		{Role: api.MessageRoleTool, Content: "result", ToolResult: &api.ToolResult{ToolCallID: "c", Tool: "read"}, PromptCacheBreakpoint: api.PromptCacheTierHistory},
	}
	out := ProjectMessages(msgs, MessageProjection{PromptCache: breakpointProjection(msgs)})
	count := 0
	for _, m := range out {
		if parts, ok := m.Content.([]ContentPart); ok {
			for _, p := range parts {
				if p.PromptCacheBreakpoint != nil {
					count++
				}
			}
		}
	}
	if count != 2 {
		t.Fatalf("markers = %d, want prefix and tool result: %+v", count, out)
	}
	if parts, ok := promptCacheBreakpointParts(out[0].Content, providerprofile.PromptCacheMarkerBreakpoint); !ok || len(parts) != 1 || parts[0].PromptCacheBreakpoint == nil {
		t.Fatalf("re-marking a marked part must keep one marker: %+v", parts)
	}
}

// Multi-part content keeps its parts and marks the final one, image included.
func TestPromptCacheBreakpointPartsMarkTheLastPart(t *testing.T) {
	parts, ok := promptCacheBreakpointParts([]ContentPart{
		{Type: "text", Text: "look"},
		{Type: "image_url", ImageURL: &ImageURL{URL: "data:image/png;base64,AA=="}},
	}, providerprofile.PromptCacheMarkerBreakpoint)
	if !ok || len(parts) != 2 || parts[0].PromptCacheBreakpoint != nil || parts[1].PromptCacheBreakpoint == nil || parts[1].CacheControl != nil {
		t.Fatalf("parts = %+v", parts)
	}
	ephemeral, ok := promptCacheBreakpointParts("body", providerprofile.PromptCacheMarkerCacheControl)
	if !ok || len(ephemeral) != 1 || ephemeral[0].CacheControl == nil || ephemeral[0].CacheControl.Type != cacheControlEphemeral || ephemeral[0].PromptCacheBreakpoint != nil {
		t.Fatalf("ephemeral parts = %+v", ephemeral)
	}
	if _, ok := promptCacheBreakpointParts("   ", providerprofile.PromptCacheMarkerBreakpoint); ok {
		t.Fatal("blank text must not carry a breakpoint")
	}
	if _, ok := promptCacheBreakpointParts([]ContentPart{}, providerprofile.PromptCacheMarkerBreakpoint); ok {
		t.Fatal("empty parts must not carry a breakpoint")
	}
	if _, ok := promptCacheBreakpointParts("body", providerprofile.PromptCacheMarkerNone); ok {
		t.Fatal("no marker must leave content untouched")
	}
}

func cacheProbeRequest(model string) modelcall.CompletionRequest {
	return modelcall.CompletionRequest{
		Model: model,
		Messages: []api.Message{
			{Role: api.MessageRoleSystem, Content: "stable system"},
			{Role: api.MessageRoleUser, Content: "go"},
			{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "call-1", Name: "read", Args: map[string]any{"path": "a"}}}},
			{Role: api.MessageRoleTool, Content: "result", ToolResult: &api.ToolResult{ToolCallID: "call-1", Tool: "read"}, PromptCacheBreakpoint: api.PromptCacheTierHistory},
			{Role: api.MessageRoleSystem, Content: "volatile board"},
		},
		Debug: modelcall.RequestDebug{SessionID: "sess-or"},
	}
}

func encodeOpenRouter(t *testing.T, model string) string {
	t.Helper()
	provider := New("openrouter", "https://openrouter.ai/api/v1", "key", []modelinfo.Entry{{ID: model}}).WithProfile(providerprofile.Openrouter()).WithPromptCache(shippedPromptCache(t, "openrouter"))
	body, err := encodeChatCompletionRequest(cacheProbeRequest(model), provider, false, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	return string(body)
}

// An Anthropic route behind OpenRouter takes cache_control on the marked part
// and at the top level; the session key pins the upstream.
func TestEncodeOpenRouterAnthropicRouteSendsCacheControl(t *testing.T) {
	s := encodeOpenRouter(t, "anthropic/claude-sonnet-4-6")
	for _, want := range []string{`"prompt_cache_key":"sess-or"`, `"cache_control":{"type":"ephemeral"}`} {
		if !strings.Contains(s, want) {
			t.Fatalf("missing %s: %s", want, s)
		}
	}
	if strings.Count(s, `"cache_control"`) != 2 {
		t.Fatalf("want one part marker and one top-level marker: %s", s)
	}
	if strings.Contains(s, "prompt_cache_breakpoint") || strings.Contains(s, "prompt_cache_retention") {
		t.Fatalf("anthropic route must not carry OpenAI-direct controls: %s", s)
	}
	if !strings.Contains(s, `{"role":"tool","content":[{"type":"text","text":"result","cache_control":{"type":"ephemeral"}}],"tool_call_id":"call-1"}`) {
		t.Fatalf("tool result part not marked: %s", s)
	}
}

// An OpenAI route behind OpenRouter takes the OpenAI marker, with no
// top-level cache_control and no retention.
func TestEncodeOpenRouterOpenAIRouteSendsBreakpoint(t *testing.T) {
	s := encodeOpenRouter(t, "openai/gpt-5.6")
	if !strings.Contains(s, `"prompt_cache_key":"sess-or"`) || strings.Count(s, `"prompt_cache_breakpoint"`) != 1 {
		t.Fatalf("want key and one breakpoint: %s", s)
	}
	if strings.Contains(s, "cache_control") || strings.Contains(s, "prompt_cache_retention") {
		t.Fatalf("openai route must not carry ephemeral or retention controls: %s", s)
	}
}

// Vendors with automatic caching get the session key and nothing else.
func TestEncodeOpenRouterAutomaticVendorSendsKeyOnly(t *testing.T) {
	s := encodeOpenRouter(t, "deepseek/deepseek-v3.2")
	if !strings.Contains(s, `"prompt_cache_key":"sess-or"`) {
		t.Fatalf("missing key: %s", s)
	}
	if strings.Contains(s, "cache_control") || strings.Contains(s, "prompt_cache_breakpoint") {
		t.Fatalf("automatic vendor must not carry markers: %s", s)
	}
}

// A LiteLLM route that reports prompt caching gets cache_control on the
// marked part and nothing else; a route without the fact gets nothing.
func TestEncodeLiteLLMMarksOnlyRoutesThatReportCaching(t *testing.T) {
	encode := func(t *testing.T, entry modelinfo.Entry) string {
		t.Helper()
		provider := New("litellm", "http://localhost:4000/v1", "key", []modelinfo.Entry{entry}).WithProfile(providerprofile.Litellm()).WithPromptCache(shippedPromptCache(t, "litellm-proxy"))
		body, err := encodeChatCompletionRequest(cacheProbeRequest(entry.ID), provider, false, controlOpts{})
		if err != nil {
			t.Fatalf("encode: %v", err)
		}
		return string(body)
	}
	caching := modelinfo.Entry{ID: "claude-alias", Capabilities: modelinfo.ModelCapabilities{
		PromptCaching: modelinfo.Evidence(modelinfo.CapabilitySupported, "litellm"),
	}}
	s := encode(t, caching)
	if !strings.Contains(s, `{"role":"tool","content":[{"type":"text","text":"result","cache_control":{"type":"ephemeral"}}],"tool_call_id":"call-1"}`) {
		t.Fatalf("tool result part not marked: %s", s)
	}
	if strings.Count(s, `"cache_control"`) != 1 || strings.Contains(s, "prompt_cache_key") || strings.Contains(s, "prompt_cache_retention") || strings.Contains(s, "prompt_cache_breakpoint") {
		t.Fatalf("litellm must send one part marker and no OpenAI-direct controls: %s", s)
	}
	s = encode(t, modelinfo.Entry{ID: "plain-alias"})
	if strings.Contains(s, "cache_control") || strings.Contains(s, "prompt_cache") {
		t.Fatalf("a route without the fact must send nothing: %s", s)
	}
}

func TestEncodeOpenAIOlderModelRetentionNoBreakpoint(t *testing.T) {
	provider := New("openai", "https://api.openai.com/v1", "key", []modelinfo.Entry{
		{ID: "gpt-5.4"},
	}).WithProfile(providerprofile.OpenAI()).WithPromptCache(shippedPromptCache(t, "openai"))
	req := modelcall.CompletionRequest{
		Model: "gpt-5.4",
		Messages: []api.Message{
			{Role: api.MessageRoleSystem, Content: "stable", PromptCacheBreakpoint: api.PromptCacheTierHistory},
			{Role: api.MessageRoleUser, Content: "go"},
		},
		Debug: modelcall.RequestDebug{SessionID: "sess-54"},
	}
	body, err := encodeChatCompletionRequest(req, provider, false, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, `"prompt_cache_retention":"24h"`) && !strings.Contains(s, `"prompt_cache_retention": "24h"`) {
		t.Fatalf("gpt-5.4 should send prompt_cache_retention 24h: %s", s)
	}
	if strings.Contains(s, "prompt_cache_breakpoint") {
		t.Fatalf("pre-5.6 models must not send breakpoints: %s", s)
	}
}

func TestEncodeAzureOmitsBreakpointsEvenOnGPT56(t *testing.T) {
	provider := New("azure", "https://example.openai.azure.com", "key", []modelinfo.Entry{
		{ID: "gpt-5.6"},
	}).WithProfile(providerprofile.Azure()).WithPromptCache(shippedPromptCache(t, "azure"))
	req := modelcall.CompletionRequest{
		Model: "gpt-5.6",
		Messages: []api.Message{
			{Role: api.MessageRoleSystem, Content: "stable", PromptCacheBreakpoint: api.PromptCacheTierHistory},
			{Role: api.MessageRoleUser, Content: "go"},
		},
		Debug: modelcall.RequestDebug{SessionID: "sess-az"},
	}
	body, err := encodeChatCompletionRequest(req, provider, false, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, `"prompt_cache_key":"sess-az"`) && !strings.Contains(s, `"prompt_cache_key": "sess-az"`) {
		t.Fatalf("azure should send prompt_cache_key: %s", s)
	}
	if strings.Contains(s, "prompt_cache_breakpoint") {
		t.Fatalf("azure must not send prompt_cache_breakpoint: %s", s)
	}
}

func TestEncodeAzureRetention24hPre56(t *testing.T) {
	provider := New("azure", "https://example.openai.azure.com", "key", []modelinfo.Entry{
		{ID: "gpt-5.4"},
	}).WithProfile(providerprofile.Azure()).WithPromptCache(shippedPromptCache(t, "azure"))
	req := modelcall.CompletionRequest{
		Model:    "gpt-5.4",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		Debug:    modelcall.RequestDebug{SessionID: "sess-az-54"},
	}
	body, err := encodeChatCompletionRequest(req, provider, false, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	s := string(body)
	if !strings.Contains(s, `"prompt_cache_retention":"24h"`) && !strings.Contains(s, `"prompt_cache_retention": "24h"`) {
		t.Fatalf("azure gpt-5.4 should send prompt_cache_retention 24h: %s", s)
	}
}

func TestEncodeGeminiOmitsPromptCacheKey(t *testing.T) {
	provider := New("gemini", "https://generativelanguage.googleapis.com/v1beta/openai", "key", []modelinfo.Entry{
		{ID: "gemini-3.5-flash"},
	}).WithProfile(providerprofile.Gemini()).WithPromptCache(shippedPromptCache(t, "gemini"))
	req := modelcall.CompletionRequest{
		Model:    "gemini-3.5-flash",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		Debug:    modelcall.RequestDebug{SessionID: "sess-gemini"},
	}
	body, err := encodeChatCompletionRequest(req, provider, false, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if strings.Contains(string(body), "prompt_cache_key") {
		t.Fatalf("gemini must not send prompt_cache_key: %s", body)
	}
}

func TestEncodeOpenAIAutomaticPrefixPromptCacheKey(t *testing.T) {
	provider := New("fireworks", "https://api.fireworks.ai/inference/v1", "key", []modelinfo.Entry{
		{ID: "accounts/fireworks/models/kimi-k2p6"},
	}).WithProfile(func() providerprofile.Profile {
		profile := providerprofile.OpenAI()
		profile.Thinking = modelinfo.ThinkStyleThinkingType
		return profile
	}()).WithPromptCache(shippedPromptCache(t, "fireworks"))
	req := modelcall.CompletionRequest{
		Model:    "accounts/fireworks/models/kimi-k2p6",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		Debug:    modelcall.RequestDebug{SessionID: "sess-kimi"},
	}
	body, err := encodeChatCompletionRequest(req, provider, false, controlOpts{})
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if !strings.Contains(string(body), `"prompt_cache_key":"sess-kimi"`) && !strings.Contains(string(body), `"prompt_cache_key": "sess-kimi"`) {
		t.Fatalf("automatic_prefix should send prompt_cache_key: %s", body)
	}
}

func TestTokenUsageFromOpenAICachedTokens(t *testing.T) {
	u := NormalizeUsage(&Usage{
		PromptTokens:     new(100),
		CompletionTokens: new(20),
		PromptTokensDetails: &PromptDetails{
			CachedTokens: 40,
		},
	})
	if u.CacheReadInputTokens != 40 {
		t.Fatalf("CacheReadInputTokens = %d, want 40", u.CacheReadInputTokens)
	}
}
