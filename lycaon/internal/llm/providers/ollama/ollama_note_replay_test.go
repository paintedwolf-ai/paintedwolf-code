package ollama

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOllamaNoteReplayKeepsCompletedToolBoundary(t *testing.T) {
	messages := []api.Message{
		{Role: api.MessageRoleUser, Content: "Run the smoke test."},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{Name: "surface_note", Args: map[string]any{"summary": "Smoke passed."}}}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "surface_note", Content: `{"status":"noted"}`}},
		{Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote, Content: "Smoke passed."},
		{Role: api.MessageRoleSystem, Content: "Current host state."},
	}
	wire := ProjectMessages(messages, false, "")
	if len(wire) != 4 || wire[2].Role != "tool" || wire[2].ToolName != "surface_note" || wire[3].Role != "user" {
		t.Fatalf("note echo changed the completed tool boundary: %+v", wire)
	}
	if wire[1].ToolCalls[0].Function.Arguments["summary"] != "Smoke passed." {
		t.Fatal("published note content was lost from the original tool call")
	}
	if len(messages) != 5 || messages[3].Content != "Smoke passed." {
		t.Fatal("provider projection changed the user-visible transcript")
	}
	withoutEcho := append(append([]api.Message(nil), messages[:3]...), messages[4:]...)
	if estimatePromptTokens(modelcall.CompletionRequest{Messages: messages}) != estimatePromptTokens(modelcall.CompletionRequest{Messages: withoutEcho}) {
		t.Fatal("context sizing counted a presentation echo absent from the wire")
	}
	messages = append(messages, api.Message{Role: api.MessageRoleAssistant, Content: "The final answer."})
	wire = ProjectMessages(messages, false, "")
	if wire[len(wire)-1].Content != "The final answer." {
		t.Fatal("ordinary assistant history was removed")
	}
}

func TestOllamaToolResultsCarryNativeNames(t *testing.T) {
	wire := ProjectMessages([]api.Message{
		{Role: api.MessageRoleTool, Content: "first result", ToolResult: &api.ToolResult{Tool: "read"}},
		{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "command", Content: "second result"}},
	}, false, "")
	encoded, err := json.Marshal(wire)
	testutil.FailErr(t, "encode native tool results", err)
	var decoded []map[string]any
	testutil.FailErr(t, "decode native tool results", json.Unmarshal(encoded, &decoded))
	if decoded[0]["tool_name"] != "read" || decoded[1]["tool_name"] != "command" || decoded[1]["content"] != "second result" {
		t.Fatalf("native tool association lost: %s", encoded)
	}
}
