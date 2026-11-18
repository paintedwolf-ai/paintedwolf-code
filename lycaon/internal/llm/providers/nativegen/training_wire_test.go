package nativegen

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

// templateInput is the chat-template input of one message: what training
// decodes from the captured wire and what the native runtime renders. Call
// arguments are objects in both; the tool name is the native runtime's own
// addition, which the template checks against the call.
type templateInput struct {
	Role       string         `json:"role"`
	Content    string         `json:"content"`
	ToolCalls  []templateCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type templateCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	} `json:"function"`
}

// trainingInput decodes a captured chat-completions request as training does:
// absent content is empty text and call arguments are parsed from their JSON text.
func trainingInput(t *testing.T, raw []byte) []templateInput {
	t.Helper()
	var wire struct {
		Messages []struct {
			Role       string  `json:"role"`
			Content    *string `json:"content"`
			ToolCallID string  `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		testutil.FailErr(t, "decode captured request", err)
	}
	out := make([]templateInput, 0, len(wire.Messages))
	for _, message := range wire.Messages {
		item := templateInput{Role: message.Role, ToolCallID: message.ToolCallID}
		if message.Content != nil {
			item.Content = *message.Content
		}
		for _, call := range message.ToolCalls {
			decoded := templateCall{ID: call.ID, Type: call.Type}
			decoded.Function.Name = call.Function.Name
			decoded.Function.Arguments = json.RawMessage(call.Function.Arguments)
			item.ToolCalls = append(item.ToolCalls, decoded)
		}
		out = append(out, item)
	}
	return out
}

func canonical(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		testutil.FailErr(t, "encode template input", err)
	}
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		testutil.FailErr(t, "decode template input", err)
	}
	out, err := json.Marshal(generic)
	if err != nil {
		testutil.FailErr(t, "encode template input", err)
	}
	return out
}

// A student is trained on requests captured from a local chat-completions
// provider and served by the native runtime. One request must reach the
// template as the same messages through both.
func TestNativeRuntimeRendersTheMessagesTrainingCaptured(t *testing.T) {
	call := func(id, path string) api.ToolCall {
		return api.ToolCall{ID: id, Name: "read_file", Args: map[string]any{"path": path, "range": []any{1, 4}}}
	}
	result := func(id, content string) api.Message {
		return api.Message{Role: api.MessageRoleTool, ToolResult: &api.ToolResult{Tool: "read_file", ToolCallID: id, Content: content}}
	}
	req := modelcall.CompletionRequest{
		Model: "student",
		Messages: []api.Message{
			{Role: api.MessageRoleSystem, Content: "Guide."},
			{Role: api.MessageRoleSystem, Content: "Standing facts."},
			result("compacted", "a result whose call was compacted away"),
			{Role: api.MessageRoleUser, Content: "  Read café.rs \n"},
			{Role: api.MessageRoleAssistant, Kind: api.MessageKindAgentNote, Content: "Checking."},
			{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{call("c1", "café.rs"), call("c2", "b.rs")}},
			result("c1", "fn main() {}\n"),
			{Role: api.MessageRoleSystem, Content: "Host event."},
			{Role: api.MessageRoleUser, Content: "Wait."},
			result("c2", "mod b;"),
			{Role: api.MessageRoleAssistant, Content: "Reading again.", ToolCalls: []api.ToolCall{call("c3", "c.rs")}},
			result("c2", "a result of an earlier group"),
			result("c3", ""),
			{Role: api.MessageRoleAssistant, Content: "Done."},
			{Role: api.MessageRoleUser, Content: "Thanks."},
		},
		Tools: []tools.ToolMeta{{Name: "read_file", Description: "Read text.", ArgsSchema: map[string]any{"type": "object"}}},
	}
	local := openaicompat.New("student", "http://127.0.0.1:1", "", []modelinfo.Entry{{ID: "student"}}).
		WithProfile(providerprofile.LocalInference(providerprofile.OpenAI()))
	captured, err := local.RenderRequest(context.Background(), req)
	if err != nil {
		testutil.FailErr(t, "render the captured request", err)
	}
	native, err := prepareRender(req)
	if err != nil {
		testutil.FailErr(t, "prepare the native request", err)
	}
	served := make([]templateInput, 0, len(native.Messages))
	for _, message := range native.Messages {
		if message.Role == string(api.MessageRoleTool) && message.Name != "read_file" {
			t.Fatalf("tool result lost its tool: %+v", message)
		}
		message.Name = ""
		var item templateInput
		if err := json.Unmarshal(canonical(t, message), &item); err != nil {
			testutil.FailErr(t, "decode native message", err)
		}
		served = append(served, item)
	}
	want, got := canonical(t, trainingInput(t, captured)), canonical(t, served)
	if !bytes.Equal(want, got) {
		t.Fatalf("native runtime renders other messages than training captured:\ncaptured %s\nnative   %s", want, got)
	}
	if len(served) != 11 {
		t.Fatalf("unpaired tool results were not dropped: %d messages", len(served))
	}
}
