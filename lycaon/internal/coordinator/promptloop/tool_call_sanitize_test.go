package promptloop

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSanitizeToolCallsForExecutionDropsEmptyArgPhantom(t *testing.T) {
	valid := api.ToolCall{
		ID:   "call_valid",
		Name: "task",
		Args: taskCallArgs("implementer", "work"),
	}
	phantom := api.ToolCall{
		ID:   "call_phantom",
		Name: "task",
		Args: nil,
		ExtraContent: map[string]any{
			"google": map[string]any{"thought_signature": "sig-from-phantom"},
		},
	}

	got := sanitizeToolCallsForExecution([]api.ToolCall{valid, phantom})
	if len(got) != 1 {
		t.Fatalf("calls = %d want 1", len(got))
	}
	if got[0].ID != valid.ID {
		t.Fatalf("kept call id = %q want %q", got[0].ID, valid.ID)
	}
	if toolCallThoughtSignature(got[0]) != "sig-from-phantom" {
		t.Fatalf("thought_signature = %q want sig migrated from phantom", toolCallThoughtSignature(got[0]))
	}
}

func TestSanitizeToolCallsForExecutionDedupesIdenticalCalls(t *testing.T) {
	args := map[string]any{"path": "a.go"}
	first := api.ToolCall{ID: "tc1", Name: "read", Args: args}
	dup := api.ToolCall{ID: "tc2", Name: "read", Args: map[string]any{"path": "a.go"}}

	got := sanitizeToolCallsForExecution([]api.ToolCall{first, dup})
	if len(got) != 1 {
		t.Fatalf("calls = %d want 1", len(got))
	}
	if got[0].ID != first.ID {
		t.Fatalf("kept call id = %q want first duplicate", got[0].ID)
	}
}

func TestSanitizeToolCallsForExecutionKeepsTruncatedCall(t *testing.T) {
	truncated := api.ToolCall{ID: "tc1", Name: "write", Args: nil, ArgsTruncated: true}
	got := sanitizeToolCallsForExecution([]api.ToolCall{truncated})
	if len(got) != 1 {
		t.Fatalf("calls = %d want truncated call kept", len(got))
	}
}

func TestSanitizeToolCallsForExecutionKeepsMalformedCall(t *testing.T) {
	// The executor returns structured feedback for malformed arguments.
	malformed := api.ToolCall{ID: "tc1", Name: "summarize", Args: nil, ArgsMalformed: true}
	sibling := api.ToolCall{ID: "tc2", Name: "summarize", Args: map[string]any{"task": "t", "path": "README.md"}}
	got := sanitizeToolCallsForExecution([]api.ToolCall{malformed, sibling})
	var keptMalformed bool
	for _, tc := range got {
		if tc.ID == "tc1" && tc.ArgsMalformed {
			keptMalformed = true
		}
	}
	if !keptMalformed {
		t.Fatalf("malformed call dropped by sanitize: %+v", got)
	}
}

func TestSanitizeToolCallsForExecutionKeepsLoneEmptyCall(t *testing.T) {
	got := sanitizeToolCallsForExecution([]api.ToolCall{
		{ID: "tc1", Name: "task", Args: map[string]any{}},
	})
	if len(got) != 1 {
		t.Fatalf("calls = %d want lone empty call kept for reject path", len(got))
	}
}

func TestToolCallHasExecutableArgs(t *testing.T) {
	if !toolCallHasExecutableArgs(map[string]any{"prompt": "go"}) {
		t.Fatal("non-empty string arg should be executable")
	}
	if toolCallHasExecutableArgs(map[string]any{"prompt": "  "}) {
		t.Fatal("whitespace-only string should not be executable")
	}
	if toolCallHasExecutableArgs(map[string]any{"scope": map[string]any{}}) {
		t.Fatal("empty nested object should not be executable")
	}
}

func TestPatchAssistantToolCalls(t *testing.T) {
	history := []api.Message{
		{ID: "a1", Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "old"}}},
	}
	sanitized := []api.ToolCall{{ID: "new", Name: "read", Args: map[string]any{"path": "x"}}}
	out := patchAssistantToolCalls(history, "a1", sanitized)
	if len(out[0].ToolCalls) != 1 || out[0].ToolCalls[0].ID != "new" {
		t.Fatalf("patched tool_calls = %+v", out[0].ToolCalls)
	}
}

func TestOfferedToolNamesEmptyIsStamped(t *testing.T) {
	got := offeredToolNames(nil)
	if got == nil {
		t.Fatal("empty offer must stamp a non-nil slice so prose-only turns filter")
	}
	if len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestEmptyToolOfferReplacesPriorTurnAllowlist(t *testing.T) {
	st := &promptLoopTurnState{offeredToolNames: []string{"read"}}
	st.stampOfferedToolNames(nil)
	if st.offeredToolNames == nil || len(st.offeredToolNames) != 0 {
		t.Fatalf("offered tools = %v, want stamped empty allowlist", st.offeredToolNames)
	}
}

func TestRetainOfferedToolCallsDropsUnoffered(t *testing.T) {
	calls := []api.ToolCall{
		{ID: "tc1", Name: "read", Args: map[string]any{"path": "x.go"}},
		{ID: "tc2", Name: "update_progress", Args: map[string]any{"content": "x"}},
	}
	got := retainOfferedToolCalls(nil, "a1", calls, []string{"read"})
	if len(got) != 1 || got[0].ID != "tc1" {
		t.Fatalf("kept = %+v want only read", got)
	}
}

func TestRetainOfferedToolCallsEmptyOfferKeepsAnsweredOnly(t *testing.T) {
	calls := []api.ToolCall{
		{ID: "tc1", Name: "read"},
		{ID: "tc2", Name: "update_progress"},
	}
	history := []api.Message{{
		Role:       api.MessageRoleTool,
		ToolResult: &api.ToolResult{AssistantMessageID: "a1", ToolCallID: "tc1"},
	}}
	got := retainOfferedToolCalls(history, "a1", calls, []string{})
	if len(got) != 1 || got[0].ID != "tc1" {
		t.Fatalf("empty offer must keep answered only, got %+v", got)
	}
}

func TestRetainOfferedToolCallsNilOfferKeepsAll(t *testing.T) {
	calls := []api.ToolCall{
		{ID: "tc1", Name: "read"},
		{ID: "tc2", Name: "update_progress"},
	}
	got := retainOfferedToolCalls(nil, "a1", calls, nil)
	if len(got) != 2 {
		t.Fatalf("nil offer must leave calls unchanged, got %+v", got)
	}
}

func TestSanitizeToolCallsForExecutionPreservesParallelDistinctTasks(t *testing.T) {
	calls := []api.ToolCall{
		{ID: "tc1", Name: "task", Args: taskCallArgs("a", "one")},
		{ID: "tc2", Name: "task", Args: taskCallArgs("b", "two")},
	}
	got := sanitizeToolCallsForExecution(calls)
	if len(got) != 2 {
		t.Fatalf("calls = %d want 2 distinct tasks", len(got))
	}
	first := got[0].Args["brief"].(map[string]any)["goal"].(string)
	second := got[1].Args["brief"].(map[string]any)["goal"].(string)
	if strings.TrimSpace(first) == strings.TrimSpace(second) {
		t.Fatal("expected distinct task goals")
	}
}

func TestOfferedArgumentSchemasStayBoundToModelRequest(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"brief": map[string]any{"type": "object"}},
	}
	st := &promptLoopTurnState{}
	st.stampOfferedToolNames([]tools.ToolMeta{{Name: "task", ArgsSchema: schema}})
	schema["properties"].(map[string]any)["brief"].(map[string]any)["type"] = "string"
	invalid := map[string]any{"brief": "serialized charter"}
	if tools.ValidateToolArgs(st.offeredToolSchemas["task"], invalid) == nil {
		t.Fatal("changing registry metadata changed the request's argument schema")
	}
	st.stampOfferedToolNames(nil)
	if len(st.offeredToolSchemas) != 0 {
		t.Fatal("a new empty offer retained the previous request's schema")
	}
}
