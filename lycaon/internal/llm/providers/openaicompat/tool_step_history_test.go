package openaicompat

import (
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/transcript"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestNativeToolHistoryKeepsNotesAndReasoningSeparate(t *testing.T) {
	const note = "Inspect the file before editing."
	const trace = "Check the boundary."
	call := api.ToolCall{ID: "call-1", WireID: "wire-1", Name: "read", Args: map[string]any{"path": "a.go"}}
	messages := []api.Message{
		{Role: api.MessageRoleUser, Content: "Fix the parser."},
		{Role: api.MessageRoleAssistant, Content: note, ToolCalls: []api.ToolCall{call}, ModelReasoning: &api.ModelReasoning{ProviderID: "fixture", Model: "fixture-model", Text: trace}},
		{Role: api.MessageRoleTool, Content: "read receipt", ToolResult: &api.ToolResult{ToolCallID: call.ID, Content: "read receipt"}},
		{Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote, Content: "Found the failing branch."},
		{Role: api.MessageRoleAssistant, Content: "Fixed the parser."},
	}
	for _, style := range []providerprofile.ReasoningWireStyle{providerprofile.ReasoningWireNone, providerprofile.ReasoningWireContent, providerprofile.ReasoningWireDetails} {
		t.Run(string(style), func(t *testing.T) {
			projected := transcript.Project(messages)
			wire := ProjectMessages(projected, MessageProjection{ProviderID: "fixture", Model: "fixture-model", ReasoningWire: style, RoundTripToolCallIDs: true})
			if len(wire) != 6 || wire[0].Role != "system" {
				t.Fatalf("wire history = %+v", wire)
			}
			step := wire[2]
			if step.Role != "assistant" || step.Content != note || len(step.ToolCalls) != 1 {
				t.Fatalf("tool step lost native content or calls: %+v", step)
			}
			if step.ToolCalls[0].ID != "wire-1" || step.ToolCalls[0].Function.Name != "read" || step.ToolCalls[0].Function.Arguments != `{"path":"a.go"}` || wire[3].ToolCallID != "wire-1" || wire[3].Role != "tool" {
				t.Fatalf("tool pairing changed: %+v / %+v", step, wire[3])
			}
			wantReasoning, wantContent := "", ""
			switch style {
			case providerprofile.ReasoningWireContent:
				wantContent = trace
			case providerprofile.ReasoningWireDetails:
				wantReasoning = trace
			}
			if step.Reasoning != wantReasoning || step.ReasoningContent != wantContent {
				t.Fatalf("reasoning crossed channels: %+v", step)
			}
			if wire[4].Content != messages[3].Content || wire[5].Content != messages[4].Content {
				t.Fatal("public note or final answer changed")
			}
			if again := ProjectMessages(transcript.Project(projected), MessageProjection{ProviderID: "fixture", Model: "fixture-model", ReasoningWire: style, RoundTripToolCallIDs: true}); !reflect.DeepEqual(again, wire) {
				t.Fatal("repeated projection changed wire history")
			}
			if messages[1].Content != note || messages[1].ModelReasoning.Text != trace {
				t.Fatal("projection mutated canonical history")
			}
		})
	}
}
