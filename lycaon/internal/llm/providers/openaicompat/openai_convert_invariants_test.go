package openaicompat

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
	openai "github.com/sashabaranov/go-openai"
)

func TestConvertMessagesToolCallIDHostVsWireRoundTrip(t *testing.T) {
	// Calls and results use the same selected identity.
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "go"},
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{ID: "host_x", WireID: "prov_y", Name: "read"}},
		},
		{Role: api.MessageRoleTool, Content: "body", ToolResult: &api.ToolResult{ToolCallID: "host_x"}},
	}

	host := ProjectMessages(msgs, MessageProjection{})
	if host[1].ToolCalls[0].ID != "host_x" {
		t.Fatalf("host mode assistant call id = %q want host_x", host[1].ToolCalls[0].ID)
	}
	if host[2].ToolCallID != "host_x" {
		t.Fatalf("host mode tool result tool_call_id = %q want host_x", host[2].ToolCallID)
	}

	roundTrip := ProjectMessages(msgs, MessageProjection{RoundTripToolCallIDs: true})
	if roundTrip[1].ToolCalls[0].ID != "prov_y" {
		t.Fatalf("round-trip assistant call id = %q want prov_y", roundTrip[1].ToolCalls[0].ID)
	}
	if roundTrip[2].ToolCallID != "prov_y" {
		t.Fatalf("round-trip tool result tool_call_id = %q want prov_y", roundTrip[2].ToolCallID)
	}
}

func TestConvertMessagesRoundTripFallsBackToHostIDWhenWireIDEmpty(t *testing.T) {
	// An absent wire identity uses the host identity.
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "host_z", Name: "read"}}},
		{Role: api.MessageRoleTool, Content: "body", ToolResult: &api.ToolResult{ToolCallID: "host_z"}},
	}
	wire := ProjectMessages(msgs, MessageProjection{RoundTripToolCallIDs: true})
	if wire[0].ToolCalls[0].ID != "host_z" || wire[1].ToolCallID != "host_z" {
		t.Fatalf("expected fallback to host_z, got call=%q result=%q", wire[0].ToolCalls[0].ID, wire[1].ToolCallID)
	}
}

func TestConvertMessagesDropsExcessToolRows(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "go"},
		{
			Role:      api.MessageRoleAssistant,
			ToolCalls: []api.ToolCall{{ID: "call_a", Name: "read"}},
		},
		{Role: api.MessageRoleTool, Content: "first", ToolResult: &api.ToolResult{ToolCallID: "call_a"}},
		{Role: api.MessageRoleTool, Content: "second-orphan"},
		{Role: api.MessageRoleTool, Content: "third-orphan"},
	}
	converted := ProjectMessages(msgs, MessageProjection{})
	if len(converted) != 3 {
		t.Fatalf("len=%d", len(converted))
	}
	if converted[2].Role != openai.ChatMessageRoleTool || converted[2].ToolCallID != "call_a" {
		t.Fatalf("first tool row should bind to call_a: %+v", converted[2])
	}
}

func TestConvertMessagesToolResultFallback(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "promote"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "call_1", Name: "read"}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{ToolCallID: "call_1", Content: "from-result"}},
	}
	converted := ProjectMessages(msgs, MessageProjection{})
	if converted[2].Content != "from-result" {
		t.Fatalf("expected ToolResult.Content fallback, got %q", converted[2].Content)
	}
}

func TestConvertMessagesAssistantTurnResetsPendingQueue(t *testing.T) {
	// A new assistant turn replaces pending call identities.
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "go"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{ID: "call_a", Name: "read"},
			{ID: "call_b", Name: "read"},
		}},
		{Role: api.MessageRoleTool, Content: "a-body", ToolResult: &api.ToolResult{ToolCallID: "call_a"}},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{ID: "call_c", Name: "read"},
		}},
		{Role: api.MessageRoleTool, Content: "c-body", ToolResult: &api.ToolResult{ToolCallID: "call_c"}},
	}
	converted := ProjectMessages(msgs, MessageProjection{})
	if converted[2].ToolCallID != "call_a" {
		t.Fatalf("first tool row ToolCallID=%q want call_a", converted[2].ToolCallID)
	}
	if converted[4].ToolCallID != "call_c" {
		t.Fatalf("post-reset tool row ToolCallID=%q want call_c (call_b should be evicted by the second assistant turn)", converted[4].ToolCallID)
	}
}

func TestConvertMessagesEmptyOrphanToolDropped(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "x"},
		{Role: api.MessageRoleTool},
	}
	converted := ProjectMessages(msgs, MessageProjection{})
	if len(converted) != 1 {
		t.Fatalf("len=%d", len(converted))
	}
	if converted[0].Role != openai.ChatMessageRoleUser {
		t.Fatalf("user row role = %q", converted[0].Role)
	}
}

func TestConvertMessagesSystemRolePassesThrough(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleSystem, Content: "you are…"},
		{Role: api.MessageRoleUser, Content: "hi"},
	}
	converted := ProjectMessages(msgs, MessageProjection{})
	if converted[0].Role != openai.ChatMessageRoleSystem || converted[0].Content != "you are…" {
		t.Fatalf("system passthrough = %+v", converted[0])
	}
}

func TestConvertMessagesUnknownRoleFallsBackToUser(t *testing.T) {
	// Unknown roles project as user messages.
	msgs := []api.Message{
		{Role: api.MessageRole("future-role"), Content: "x"},
	}
	converted := ProjectMessages(msgs, MessageProjection{})
	if converted[0].Role != openai.ChatMessageRoleUser {
		t.Fatalf("unknown role should default to user, got %q", converted[0].Role)
	}
}

func TestConvertMessagesPrefersContentOverToolResult(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleUser, Content: "go"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "c1", Name: "n"}}},
		{Role: api.MessageRoleTool, Content: "primary", ToolResult: &api.ToolResult{ToolCallID: "c1", Content: "fallback"}},
	}
	converted := ProjectMessages(msgs, MessageProjection{})
	if converted[2].Content != "primary" {
		t.Fatalf("expected Content to win, got %q", converted[2].Content)
	}
}

func TestConvertMessagesNilToolArgsEncodeAsObject(t *testing.T) {
	msgs := []api.Message{
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{ID: "c1", Name: "write", Args: nil, ArgsTruncated: true},
		}},
	}
	converted := ProjectMessages(msgs, MessageProjection{})
	if len(converted[0].ToolCalls) != 1 {
		t.Fatalf("missing tool call")
	}
	if got := converted[0].ToolCalls[0].Function.Arguments; got != "{}" {
		t.Fatalf("got %q want {}", got)
	}
}
