package openaicompat

import (
	"encoding/json"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools"
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
