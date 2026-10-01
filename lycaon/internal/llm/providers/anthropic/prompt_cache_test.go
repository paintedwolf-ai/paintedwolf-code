package anthropic

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/pkg/api"
)

// testPolicy is the shipped anthropic profile: a long-lived standing prefix
// ahead of short-lived history.
var testPolicy = providerprofile.PromptCachePolicy{
	Profile: "anthropic", Mode: providerprofile.PromptCacheExplicitBreakpoints,
	Lifetime: providerprofile.PromptCacheLifetime{
		Standing: providerprofile.CacheDuration(time.Hour), History: providerprofile.CacheDuration(5 * time.Minute),
	},
}

func cachingProvider(models ...modelinfo.Entry) *Provider {
	return New("anthropic", "https://api.anthropic.com/v1", "k", models).WithPromptCache(testPolicy)
}

func TestAnthropicMarksEachTierWithItsLifetime(t *testing.T) {
	p := cachingProvider(modelinfo.Entry{ID: "claude-sonnet-5"})
	req := modelcall.CompletionRequest{
		Model: "claude-sonnet-5",
		Messages: []api.Message{
			{Role: api.MessageRoleSystem, Content: "stable", PromptCacheBreakpoint: api.PromptCacheTierStanding},
			{Role: api.MessageRoleUser, Content: "go"},
			{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "call-1", Name: "read", Args: map[string]any{"path": "a"}}}},
			{Role: api.MessageRoleTool, Content: "result", ToolResult: &api.ToolResult{ToolCallID: "call-1", Tool: "read"}, PromptCacheBreakpoint: api.PromptCacheTierHistory},
			{Role: api.MessageRoleSystem, Content: "volatile board"},
		},
	}
	built := p.Prepare(req, false)
	if built.CacheControl == nil || built.CacheControl.TTL != "5m" {
		t.Fatalf("top-level cache_control = %+v, want the history lifetime", built.CacheControl)
	}
	if len(built.System) != 1 || built.System[0].CacheControl == nil || built.System[0].CacheControl.TTL != "1h" {
		t.Fatalf("standing marker = %+v, want 1h on the system block", built.System)
	}
	var marked []ContentBlock
	for _, msg := range built.Messages {
		for _, block := range msg.Content {
			if block.CacheControl != nil {
				marked = append(marked, block)
			}
		}
	}
	if len(marked) != 1 || marked[0].Type != "tool_result" || marked[0].CacheControl.TTL != "5m" {
		t.Fatalf("history marker = %+v, want 5m on the tool result", marked)
	}
}

func TestAnthropicSendsNoCacheControlWithoutAPolicy(t *testing.T) {
	p := New("anthropic", "https://api.anthropic.com/v1", "k", nil)
	req := modelcall.CompletionRequest{
		Model:    "claude-sonnet-5",
		Messages: []api.Message{{Role: api.MessageRoleSystem, Content: "stable", PromptCacheBreakpoint: api.PromptCacheTierStanding}},
	}
	body, _ := json.Marshal(p.Prepare(req, false))
	if strings.Contains(string(body), "cache_control") {
		t.Fatalf("an unattached driver must not emit cache_control: %s", body)
	}
}

// A breakpoint on the final message still lands after the loop.
func TestAnthropicCacheControlOnLastMessageBreakpoint(t *testing.T) {
	system, out := ProjectMessages([]api.Message{
		{Role: api.MessageRoleSystem, Content: "stable"},
		{Role: api.MessageRoleUser, Content: "go"},
	}, []providerwire.PromptCacheBreakpoint{{Index: 1, Tier: api.PromptCacheTierHistory, Lifetime: 5 * time.Minute}}, false, "", "anthropic", "claude-sonnet-5")
	if system[0].CacheControl != nil {
		t.Fatal("system block must not carry the breakpoint")
	}
	last := out[len(out)-1].Content
	if control := last[len(last)-1].CacheControl; control == nil || control.TTL != "5m" {
		t.Fatalf("last user block cache_control = %+v", control)
	}
}

// Only the boundaries nearest the tail survive the transport's slot count.
func TestAnthropicMarksEveryBoundaryUpToTheLimit(t *testing.T) {
	bp := func(i int) providerwire.PromptCacheBreakpoint {
		return providerwire.PromptCacheBreakpoint{Index: i, Tier: api.PromptCacheTierHistory}
	}
	msgs := []api.Message{
		{Role: api.MessageRoleSystem, Content: "core"},
		{Role: api.MessageRoleSystem, Content: "prefix"},
		{Role: api.MessageRoleUser, Content: "one"},
		{Role: api.MessageRoleAssistant, Content: "a"},
		{Role: api.MessageRoleUser, Content: "two"},
		{Role: api.MessageRoleAssistant, Content: "b"},
		{Role: api.MessageRoleUser, Content: "three"},
		{Role: api.MessageRoleSystem, Content: "volatile"},
	}
	all := []providerwire.PromptCacheBreakpoint{bp(1), bp(2), bp(4), bp(6)}
	system, out := ProjectMessages(msgs, providerwire.TrailingPromptCacheBreakpoints(all, providerwire.AnthropicExplicitBreakpointLimit), false, "", "anthropic", "claude-sonnet-5")
	if system[1].CacheControl != nil {
		t.Fatal("the prefix marker must yield to the three history markers")
	}
	var marked []string
	for _, m := range out {
		for _, b := range m.Content {
			if b.CacheControl != nil {
				marked = append(marked, b.Text)
			}
		}
	}
	if len(marked) != 3 || marked[0] != "one" || marked[1] != "two" || marked[2] != "three" {
		t.Fatalf("marked = %v, want the three history boundaries", marked)
	}
}

// Thinking blocks cannot carry cache_control; the marker walks back past them.
func TestMarkLastCacheableBlockSkipsThinking(t *testing.T) {
	control := &anthropicCacheControl{Type: "ephemeral", TTL: "5m"}
	blocks := []ContentBlock{
		{Type: "text", Text: "answer"},
		{Type: "thinking", Thinking: "reasoning", Signature: "sig"},
		{Type: "redacted_thinking", Data: "opaque"},
	}
	if !markLastCacheableBlock(blocks, control) {
		t.Fatal("expected a cacheable block")
	}
	if blocks[0].CacheControl == nil || blocks[1].CacheControl != nil || blocks[2].CacheControl != nil {
		t.Fatalf("cache_control placement wrong: %+v", blocks)
	}
	if markLastCacheableBlock([]ContentBlock{{Type: "thinking", Thinking: "only"}}, control) {
		t.Fatal("thinking-only content must report no cacheable block")
	}
}
