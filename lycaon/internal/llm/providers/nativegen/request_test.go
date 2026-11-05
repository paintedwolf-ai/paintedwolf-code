package nativegen

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
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
		testutil.FailErr(t, "prepare tool history", err)
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
	wantMessages := wire.Messages
	req.ToolUse = modelcall.ToolUseAllowed
	for _, format := range []*modelcall.ResponseFormat{
		{Type: modelcall.ResponseFormatJSONObject},
		modelcall.JSONSchemaFormat("answer", json.RawMessage(`{"type":"object"}`)),
	} {
		req.ResponseFormat = format
		wire, err = prepareRender(req)
		if err != nil || len(wire.Tools) != 0 || !reflect.DeepEqual(wire.Messages, wantMessages) {
			t.Fatalf("structured-output projection changed history or offered tools: %+v, %v", wire, err)
		}
		if len(req.Tools) != 1 || req.Tools[0].Name != "read_file" {
			t.Fatal("projection mutated request tools")
		}
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
		testutil.FailErr(t, "prepare host event", err)
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
	provider, runtime := fixtureProvider(t)
	runtime.config.ContextLimit = 8192
	for _, requested := range []int{0, 1024, 99999} {
		req := modelcall.CompletionRequest{MaxTokens: requested}
		budget := provider.RequestBudget(t.Context(), req)
		want := DefaultOutputReserveTokens
		if requested > 0 {
			want = min(requested, MaxOutputReserveTokens)
		}
		if budget.Err != nil || !budget.Exact || budget.Input != 3 || budget.Headroom != 0 || budget.Window != 8192 || budget.Output != want {
			t.Fatalf("request %d budget = %+v", requested, budget)
		}
	}
	entry, ok := provider.ModelEntry(DefaultModelID)
	if !ok || entry.ContextLength != 8192 || DefaultModelEntry().ContextLength != 65536 {
		t.Fatalf("runtime window changed artifact metadata: %+v", entry)
	}
}

func TestPrepareRenderUsesAssemblyBoundaryAfterProjection(t *testing.T) {
	req := modelcall.CompletionRequest{Messages: []api.Message{
		{Role: api.MessageRoleSystem, Content: "Foundation"},
		{Role: api.MessageRoleSystem, Content: "Project", PromptCacheBreakpoint: api.PromptCacheTierStanding},
		{Role: api.MessageRoleUser, Content: "Question", PromptCacheBreakpoint: api.PromptCacheTierHistory},
	}}
	rendered, err := prepareRender(req)
	if err != nil || rendered.CheckpointMessages != 1 || rendered.KnownPrefixMessages != 2 {
		t.Fatalf("merged standing boundary: %+v, %v", rendered, err)
	}
	req.Messages[1].PromptCacheBreakpoint = api.PromptCacheTierNone
	req.Messages[2].PromptCacheBreakpoint = api.PromptCacheTierStanding
	rendered, err = prepareRender(req)
	if err != nil || rendered.CheckpointMessages != 2 || rendered.KnownPrefixMessages != 2 {
		t.Fatalf("moved standing boundary: %+v, %v", rendered, err)
	}
	req.Messages[2].PromptCacheBreakpoint = api.PromptCacheTierNone
	rendered, err = prepareRender(req)
	if err != nil || rendered.CheckpointMessages != 0 || rendered.KnownPrefixMessages != 0 {
		t.Fatalf("invented unmarked boundary: %+v, %v", rendered, err)
	}
}

func TestNativeProjectionOmitsCapturedThoughtWithoutMutatingHistory(t *testing.T) {
	for _, toolLoop := range []bool{false, true} {
		reasoning := &api.ModelReasoning{ProviderID: "nativegen", Model: DefaultModelID, Text: "captured-thought"}
		assistant := api.Message{Role: api.MessageRoleAssistant, Content: "Answer", ModelReasoning: reasoning}
		next := api.Message{Role: api.MessageRoleUser, Content: "Next question"}
		if toolLoop {
			assistant.ToolCalls = []api.ToolCall{{ID: "call_1", Name: "read_file", Args: map[string]any{"path": "source.go"}}}
			next = api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "read_file", ToolCallID: "call_1", Content: "result"}}
		}
		req := modelcall.CompletionRequest{Messages: []api.Message{{Role: api.MessageRoleUser, Content: "Question"}, assistant, next}, Tools: []tools.ToolMeta{{Name: "read_file"}}}
		wire, err := prepareRender(req)
		if err != nil {
			t.Fatalf("project native history: %v", err)
		}
		raw, err := json.Marshal(wire)
		if err != nil || strings.Contains(string(raw), "captured-thought") {
			t.Fatalf("native wire replayed captured thought: %s, %v", raw, err)
		}
		if req.Messages[1].ModelReasoning != reasoning || reasoning.Text != "captured-thought" {
			t.Fatal("projection mutated captured reasoning")
		}
	}
}
