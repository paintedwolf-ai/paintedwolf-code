package bedrock

import (
	"testing"
	"time"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/pkg/api"
)

func cachePoints(content []brtypes.ContentBlock) int {
	n := 0
	for _, b := range content {
		if _, ok := b.(*brtypes.ContentBlockMemberCachePoint); ok {
			n++
		}
	}
	return n
}

// A family whose host fixes the checkpoint lifetime, such as Nova, receives
// the checkpoint with no ttl: Converse refuses a named lifetime there.
func TestBedrockCheckpointWithoutLifetimeCarriesNoTTL(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleSystem, Content: "core"},
		{Role: api.MessageRoleUser, Content: "hello"},
	}
	system, out := ProjectMessages(msgs, []providerwire.PromptCacheBreakpoint{
		{Index: 0, Tier: api.PromptCacheTierStanding},
	}, false, "")
	if len(system) != 2 {
		t.Fatalf("system = %+v, want text then checkpoint", system)
	}
	point, ok := system[1].(*brtypes.SystemContentBlockMemberCachePoint)
	if !ok || point.Value.Type != brtypes.CachePointTypeDefault || point.Value.Ttl != "" {
		t.Fatalf("system[1] = %#v, want a default cache point with no ttl", system[1])
	}
	if len(out) != 1 || cachePoints(out[0].Content) != 0 {
		t.Fatalf("messages = %+v, want the user turn without a checkpoint", out)
	}
}

// A checkpoint on a preamble row closes the system prompt; one on a tool
// result follows that block; a later system row rides in conversation order.
func TestBedrockCheckpointsFollowMarkedRows(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleSystem, Content: "core"},
		{Role: api.MessageRoleSystem, Content: "prefix"},
		{Role: api.MessageRoleUser, Content: "read main.go"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "h1", WireID: "tooluse_1", Name: "read", Args: map[string]any{"path": "main.go"}}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "h1", Content: "package main"}},
		{Role: api.MessageRoleSystem, Content: "volatile board"},
	}
	system, out := ProjectMessages(msgs, []providerwire.PromptCacheBreakpoint{
		{Index: 1, Tier: api.PromptCacheTierStanding, Lifetime: time.Hour},
		{Index: 4, Tier: api.PromptCacheTierHistory, Lifetime: 5 * time.Minute},
	}, false, "")
	if len(system) != 2 {
		t.Fatalf("system = %+v, want text then checkpoint", system)
	}
	if text := system[0].(*brtypes.SystemContentBlockMemberText).Value; text != "core\n\nprefix" {
		t.Fatalf("system text = %q", text)
	}
	if point, ok := system[1].(*brtypes.SystemContentBlockMemberCachePoint); !ok || point.Value.Ttl != brtypes.CacheTTLOneHour {
		t.Fatalf("system[1] = %#v, want a 1h cache point", system[1])
	}
	if len(out) != 3 {
		t.Fatalf("messages = %d, want user, assistant, and one user turn holding the tool result and the trailing system row", len(out))
	}
	// Converse alternates roles, so the volatile system row joins the tool
	// result's user turn; the checkpoint sits between them.
	result := out[2]
	if result.Role != brtypes.ConversationRoleUser || len(result.Content) != 3 || cachePoints(result.Content) != 1 {
		t.Fatalf("tool result turn = %+v, want tool result, cache point, volatile text", result)
	}
	if point, ok := result.Content[1].(*brtypes.ContentBlockMemberCachePoint); !ok || point.Value.Ttl != brtypes.CacheTTLFiveMinutes {
		t.Fatalf("a 5m checkpoint must follow the tool result: %+v", result.Content)
	}
	if text := result.Content[2].(*brtypes.ContentBlockMemberText).Value; text != "volatile board" {
		t.Fatalf("volatile text = %q, want it after the checkpoint", text)
	}
}

// Marks on adjacent rows that share one turn emit one checkpoint.
func TestBedrockCheckpointDedupesWithinATurn(t *testing.T) {
	both := []providerwire.PromptCacheBreakpoint{{Index: 0}, {Index: 1}}
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "a"},
		{Role: api.MessageRoleUser, Content: "b"},
	}
	_, out := ProjectMessages(msgs, both, false, "")
	if len(out) != 1 || cachePoints(out[0].Content) != 2 {
		t.Fatalf("turn = %+v, want a checkpoint after each text block", out)
	}
	msgs = []api.Message{
		{Role: api.MessageRoleUser, Content: "a"},
		{Role: api.MessageRoleAssistant, ToolCalls: nil},
	}
	_, out = ProjectMessages(msgs, both, false, "")
	if len(out) != 1 || cachePoints(out[0].Content) != 1 {
		t.Fatalf("empty assistant must not add a second checkpoint: %+v", out)
	}
	if point := out[0].Content[1].(*brtypes.ContentBlockMemberCachePoint); point.Value.Ttl != "" {
		t.Fatalf("a boundary without a lifetime takes the provider default: %+v", point.Value)
	}
}
