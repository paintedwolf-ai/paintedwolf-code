package anthropic

import (
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAnthropicMessagesFromHostSplitsSystemAndPairsTools(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleSystem, Content: "be terse"},
		{Role: api.MessageRoleSystem, Content: "cite sources"},
		{Role: api.MessageRoleUser, Content: "read main.go"},
		{Role: api.MessageRoleAssistant, Content: "reading", ToolCalls: []api.ToolCall{
			{ID: "host-1", WireID: "toolu_abc", Name: "read", Args: map[string]any{"path": "main.go"}},
		}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "host-1", Content: "package main"}},
	}

	system, out := ProjectMessages(msgs, nil, false, "", "", "")
	if len(system) != 2 {
		t.Fatalf("system blocks = %d, want 2", len(system))
	}
	if system[0].Text != "be terse" || system[1].Text != "cite sources" {
		t.Fatalf("system blocks = %+v", system)
	}
	if len(out) != 3 {
		t.Fatalf("messages = %d, want 3", len(out))
	}
	if out[0].Role != "user" || out[0].Content[0].Text != "read main.go" {
		t.Fatalf("first turn = %+v", out[0])
	}
	asst := out[1]
	if asst.Role != "assistant" || len(asst.Content) != 2 {
		t.Fatalf("assistant turn = %+v", asst)
	}
	if asst.Content[1].Type != "tool_use" || asst.Content[1].ID != "toolu_abc" {
		t.Fatalf("tool_use block = %+v", asst.Content[1])
	}
	result := out[2]
	if result.Role != "user" || result.Content[0].Type != "tool_result" {
		t.Fatalf("tool_result turn = %+v", result)
	}
	if result.Content[0].ToolUseID != "toolu_abc" {
		t.Fatalf("tool_result tool_use_id = %q, want toolu_abc (round-tripped wire id)", result.Content[0].ToolUseID)
	}
	if result.Content[0].ResultText != "package main" {
		t.Fatalf("tool_result content = %q", result.Content[0].ResultText)
	}
}

func TestAnthropicMessagesCoalesceConsecutiveUser(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "one"},
		{Role: api.MessageRoleUser, Content: "two"},
	}
	_, out := ProjectMessages(msgs, nil, false, "", "", "")
	if len(out) != 1 || len(out[0].Content) != 2 {
		t.Fatalf("want one coalesced user turn with 2 blocks, got %+v", out)
	}
}

func TestAnthropicMessagesDropOrphanToolResult(t *testing.T) {
	_, out := ProjectMessages([]api.Message{
		{Role: api.MessageRoleUser, Content: "hello"},
		{Role: api.MessageRoleTool, Content: "stray output"},
	}, nil, false, "", "", "")
	if len(out) != 1 || out[0].Role != "user" || len(out[0].Content) != 1 || out[0].Content[0].Text != "hello" {
		t.Fatalf("out = %+v", out)
	}
}

func TestMapAnthropicResponseSplitsTextThinkingTools(t *testing.T) {
	resp := &Response{
		Content: []ContentBlock{
			{Type: "thinking", Thinking: "let me look"},
			{Type: "text", Text: "done"},
			{Type: "tool_use", ID: "toolu_x", Name: "grep", Input: map[string]any{"q": "foo"}},
		},
		StopReason: "tool_use",
		Usage:      &Usage{InputTokens: new(10), OutputTokens: new(5), CacheReadInputTokens: 3},
	}
	out, err := mapAnthropicResponse(resp)
	if err != nil {
		t.Fatalf("map response: %v", err)
	}
	if out.Content != "done" || out.Reasoning != "let me look" {
		t.Fatalf("content/reasoning = %q / %q", out.Content, out.Reasoning)
	}
	if len(out.ToolCalls) != 1 || out.ToolCalls[0].WireID != "toolu_x" || out.ToolCalls[0].Name != "grep" {
		t.Fatalf("tool calls = %+v", out.ToolCalls)
	}
	if out.ToolCalls[0].ID == "" || out.ToolCalls[0].ID == "toolu_x" {
		t.Fatalf("expected host-minted id distinct from wire id, got %q", out.ToolCalls[0].ID)
	}
	// PromptTokens is the inclusive input total: input_tokens 10 + 3 cache reads.
	if out.Usage.PromptTokens != 13 || out.Usage.CompletionTokens != 5 || out.Usage.CacheReadInputTokens != 3 {
		t.Fatalf("usage = %+v", out.Usage)
	}
}

func TestAnthropicBuildRequestThinkingFromOverride(t *testing.T) {
	p := New("anthropic", "https://api.anthropic.com/v1", "k", []modelinfo.Entry{
		{ID: "claude-opus-4-8", ReasoningEffort: "high", Temperature: ptrFloat(0.7)},
	})
	req := modelcall.CompletionRequest{
		Model:    "claude-opus-4-8",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "hi"}},
		Tools:    []tools.ToolMeta{{Name: "read", ArgsSchema: map[string]any{"type": "object"}}},
	}
	got := p.Prepare(req, false)
	if got.Thinking == nil || got.Thinking.BudgetTokens != 16384 {
		t.Fatalf("thinking = %+v, want enabled budget 16384", got.Thinking)
	}
	if got.Temperature != nil {
		t.Fatalf("temperature must be cleared when thinking is on, got %v", *got.Temperature)
	}
	if got.MaxTokens <= got.Thinking.BudgetTokens {
		t.Fatalf("max_tokens %d must exceed thinking budget %d", got.MaxTokens, got.Thinking.BudgetTokens)
	}
	if len(got.Tools) != 1 || got.Tools[0].Name != "read" {
		t.Fatalf("tools = %+v", got.Tools)
	}
}

func TestAnthropicBuildRequestCapsOrchestrationMaxTokens(t *testing.T) {
	p := New("anthropic", "https://api.anthropic.com/v1", "k", []modelinfo.Entry{
		{ID: "claude-sonnet-4-6", MaxTokens: 100000},
	})
	req := modelcall.CompletionRequest{
		Model:    "claude-sonnet-4-6",
		Messages: []api.Message{{Role: api.MessageRoleUser, Content: "go"}},
		Tools:    []tools.ToolMeta{{Name: "edit", ArgsSchema: map[string]any{"type": "object"}}},
	}
	got := p.Prepare(req, false)
	if got.MaxTokens != modelcall.OrchestrationMaxTokens+8192 {
		t.Fatalf("max_tokens = %d, want orchestration cap %d", got.MaxTokens, modelcall.OrchestrationMaxTokens)
	}
	if got.Thinking == nil || got.Thinking.BudgetTokens != 8192 {
		t.Fatalf("orchestration reasoning must use the application default, got %+v", got.Thinking)
	}
}

func TestAnthropicDriverProfile(t *testing.T) {
	profile := New("anthropic", "u", "k", nil).Profile()
	if !profile.RoundTripsToolCallID() {
		t.Fatal("anthropic must round-trip tool_use ids")
	}
	if profile.Thinking != modelinfo.ThinkStyleBudgetTokens {
		t.Fatalf("thinking style = %q, want budget_tokens", profile.Thinking)
	}
	if profile.Discovery != providerprofile.DiscoveryAnthropic {
		t.Fatalf("discovery = %q, want anthropic", profile.Discovery)
	}
}

func ptrFloat(f float64) *float64 { return &f }

// Mid-conversation system rows retain their timeline position.
func TestAnthropicMessagesKeepMidConversationSystemInPlace(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleSystem, Content: "be terse"},
		{Role: api.MessageRoleUser, Content: "start the leg"},
		{Role: api.MessageRoleAssistant, Content: "working"},
		{Role: api.MessageRoleSystem, Content: "Leg finished · 2m ago"},
		{Role: api.MessageRoleUser, Content: "next"},
	}

	system, out := ProjectMessages(msgs, nil, false, "", "", "")

	// The preamble remains standing instruction.
	if len(system) != 1 || system[0].Text != "be terse" {
		t.Fatalf("system = %+v, want only the preamble row", system)
	}

	// The positioned row joins the following user turn.
	var texts []string
	for _, m := range out {
		for _, b := range m.Content {
			texts = append(texts, m.Role+":"+b.Text)
		}
	}
	want := []string{
		"user:start the leg",
		"assistant:working",
		"user:Leg finished · 2m ago",
		"user:next",
	}
	if !slices.Equal(texts, want) {
		t.Fatalf("wire turns = %v, want %v", texts, want)
	}
}
