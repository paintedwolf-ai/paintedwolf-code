package nativegen

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestPrepareRenderPreservesStructuredArgumentsAndToolIdentity(t *testing.T) {
	req := modelcall.CompletionRequest{
		Messages: []api.Message{
			{Role: api.MessageRoleSystem, Content: "System"},
			{Role: api.MessageRoleUser, Content: "Read café.rs"},
			{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "call_1", Name: "read_file", Args: map[string]any{"path": "café.rs", "range": []any{1, 4}}}}},
			{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "read_file", ToolCallID: "call_1", Content: "fn main() {}\n"}},
		},
		Tools: []tools.ToolMeta{{Name: "read_file", Description: "Read text.", ArgsSchema: map[string]any{"type": "object"}}},
	}
	wire, err := prepareRender(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire.Messages) != 4 || len(wire.Tools) != 1 {
		t.Fatalf("lost conversation structure: %+v", wire)
	}
	if wire.Messages[2].ToolCalls[0].Function.Arguments["path"] != "café.rs" || wire.Messages[3].ToolCallID != "call_1" || wire.Messages[3].Content != "fn main() {}\n" {
		t.Fatalf("changed tool replay: %+v", wire.Messages)
	}
	raw, err := json.Marshal(wire)
	if err != nil || !strings.Contains(string(raw), `"arguments":{"path"`) {
		t.Fatalf("arguments were not an object: %s, %v", raw, err)
	}
	req.ToolUse = modelcall.ToolUseForbidden
	wire, err = prepareRender(req)
	if err != nil || len(wire.Tools) != 0 || len(wire.Messages[2].ToolCalls) != 1 {
		t.Fatalf("no-call projection damaged history: %+v, %v", wire, err)
	}
}

func TestPrepareRenderKeepsHostEventsInConversationOrder(t *testing.T) {
	req := modelcall.CompletionRequest{Messages: []api.Message{
		{Role: api.MessageRoleSystem, Content: "First"},
		{Role: api.MessageRoleSystem, Content: "Second"},
		{Role: api.MessageRoleUser, Content: "Question"},
		{Role: api.MessageRoleSystem, Content: "Current host event"},
	}}
	wire, err := prepareRender(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire.Messages) != 3 || wire.Messages[0].Content != "First\n\nSecond" || wire.Messages[2].Role != "user" || wire.Messages[2].Content != "Current host event" {
		t.Fatalf("moved host event into standing prefix: %+v", wire.Messages)
	}
	if req.Messages[3].Role != api.MessageRoleSystem {
		t.Fatal("mutated durable history")
	}
}

func TestPrepareRenderRefusesIncompleteHistoryAndUnknownFormats(t *testing.T) {
	for _, messages := range [][]api.Message{
		nil,
		{{Role: "unknown", Content: "x"}},
		{{Role: api.MessageRoleTool, Content: "result"}},
		{{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "1", Name: "read_file", ArgsMalformed: true}}}},
		{{Role: api.MessageRoleUser, ToolCalls: []api.ToolCall{{ID: "1", Name: "read_file"}}}},
	} {
		if _, err := prepareRender(modelcall.CompletionRequest{Messages: messages}); err == nil {
			t.Fatalf("accepted invalid history: %+v", messages)
		}
	}
	if _, err := outputSchema(&modelcall.ResponseFormat{Type: "unknown"}); err == nil {
		t.Fatal("accepted an unknown response format")
	}
	if _, err := outputSchema(modelcall.JSONSchemaFormat("answer", json.RawMessage(`{`))); err == nil {
		t.Fatal("accepted an invalid schema")
	}
	format := modelcall.JSONSchemaFormat("answer", json.RawMessage(`{"const":4}`))
	schema, err := outputSchema(format)
	if err != nil || string(schema) != string(format.Schema) {
		t.Fatalf("schema = %s, %v", schema, err)
	}
	format.Schema[0] = '['
	if string(schema) != `{"const":4}` {
		t.Fatal("schema aliases caller memory")
	}
}

func TestRequestBudgetUsesRuntimeWindowAndNativeOutputReserve(t *testing.T) {
	runtime := NewRuntime(RuntimeConfig{ContextLimit: 8192})
	t.Cleanup(func() { _ = runtime.Close() })
	provider := New("local", Options{Runtime: runtime})
	for _, requested := range []int{0, 1024, 99999} {
		req := modelcall.CompletionRequest{MaxTokens: requested}
		budget := provider.RequestBudget(t.Context(), req)
		want := DefaultOutputReserveTokens
		if requested > 0 {
			want = min(requested, MaxOutputReserveTokens)
		}
		if budget.Window != 8192 || budget.Output != want {
			t.Fatalf("request %d budget = %+v", requested, budget)
		}
	}
	entry, ok := provider.ModelEntry(DefaultModelID)
	if !ok || entry.ContextLength != 8192 || DefaultModelEntry().ContextLength != 65536 {
		t.Fatalf("runtime window changed artifact metadata: %+v", entry)
	}
}
