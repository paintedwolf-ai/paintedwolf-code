package openaicompat

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestForbiddenToolUseKeepsDefinitionsAndSendsNoCallChoice(t *testing.T) {
	provider := New("fireworks", "https://api.fireworks.ai/inference/v1", "key", []modelinfo.Entry{{ID: "m"}})
	for _, tc := range []struct {
		use  modelcall.ToolUse
		want any
	}{
		{modelcall.ToolUseAllowed, nil},
		{modelcall.ToolUseForbidden, "none"},
	} {
		req := modelcall.CompletionRequest{Model: "m", Tools: []tools.ToolMeta{{Name: "read"}}, ToolUse: tc.use}
		body, err := encodeChatCompletionRequest(req, provider, false, controlOpts{})
		testutil.FailErr(t, "encode", err)
		var wire map[string]any
		testutil.FailErr(t, "decode", json.Unmarshal(body, &wire))
		if wire["tool_choice"] != tc.want {
			t.Fatalf("tool_use %d: tool_choice = %v, want %v", tc.use, wire["tool_choice"], tc.want)
		}
		if list, _ := wire["tools"].([]any); len(list) != 1 {
			t.Fatalf("tool_use %d: tools = %v, want the definitions kept", tc.use, wire["tools"])
		}
	}
}

func TestForbiddenToolUseOmitsDefinitionsOnLocalInferenceProfiles(t *testing.T) {
	for name, profile := range map[string]providerprofile.Profile{
		"openai-compatible": providerprofile.LocalInference(providerprofile.OpenAI()),
		"lmstudio":          providerprofile.Lmstudio(),
		"omlx":              providerprofile.Omlx(),
		"litellm":           providerprofile.Litellm(),
	} {
		t.Run(name, func(t *testing.T) {
			provider := New(name, "http://localhost:8080/v1", "", []modelinfo.Entry{{ID: "m"}}).WithProfile(profile)
			req := modelcall.CompletionRequest{
				Model: "m",
				Messages: []api.Message{
					{Role: api.MessageRoleUser, Content: "build game"},
					{
						Role:      api.MessageRoleAssistant,
						ToolCalls: []api.ToolCall{{ID: "call_1", Name: "update_progress", Args: map[string]any{"content": "- [ ] build"}}},
					},
					{
						Role:       api.MessageRoleTool,
						Content:    `{"status":"unchanged"}`,
						ToolResult: &api.ToolResult{ToolCallID: "call_1", Content: `{"status":"unchanged"}`},
					},
				},
				Tools: []tools.ToolMeta{
					{Name: "list_dir"},
					{Name: "read"},
					{Name: "request_tools"},
					{Name: "skills_read"},
					{Name: "summarize"},
					{Name: "update_progress"},
				},
			}

			allowedBody, err := encodeChatCompletionRequest(req, provider, false, controlOpts{})
			testutil.FailErr(t, "encode allowed", err)
			var allowedWire map[string]any
			testutil.FailErr(t, "decode allowed", json.Unmarshal(allowedBody, &allowedWire))
			if list, _ := allowedWire["tools"].([]any); len(list) != 6 {
				t.Fatalf("allowed tool use tools = %d, want 6", len(list))
			}

			req.ToolUse = modelcall.ToolUseForbidden
			forbiddenBody, err := encodeChatCompletionRequest(req, provider, false, controlOpts{})
			testutil.FailErr(t, "encode forbidden", err)
			var forbiddenWire map[string]any
			testutil.FailErr(t, "decode forbidden", json.Unmarshal(forbiddenBody, &forbiddenWire))
			if _, hasTools := forbiddenWire["tools"]; hasTools {
				t.Fatalf("forbidden tool use on %s sent tools: %s", name, forbiddenBody)
			}
			if _, hasChoice := forbiddenWire["tool_choice"]; hasChoice {
				t.Fatalf("forbidden tool use on %s sent tool_choice: %s", name, forbiddenBody)
			}
			msgs, _ := forbiddenWire["messages"].([]any)
			if len(msgs) != 3 {
				t.Fatalf("messages count = %d, want 3: %s", len(msgs), forbiddenBody)
			}
		})
	}
}
