package anthropic

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/tools/surveyjson"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestToolResultCarriesItsImageBlock(t *testing.T) {
	png := tinyPNG(t)
	_, out := ProjectMessages([]api.Message{
		{Role: api.MessageRoleUser, Content: "check"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "call-1", Name: "capture_page"}}},
		{Role: api.MessageRoleTool, Content: "captured", ToolResult: &api.ToolResult{
			ToolCallID: "call-1", Content: "captured",
			Visual: &api.VisualArtifact{ID: "art-1", Mime: "image/png", Bytes: png, Perceive: true},
		}},
	}, nil, true, "sess-1", "", "")
	result := out[len(out)-1].Content[0]
	raw, err := surveyjson.Marshal(result)
	testutil.FailErr(t, "marshal tool result", err)
	body := string(raw)
	if !strings.Contains(body, `"content":[{"type":"text","text":"captured"},{"type":"image","source":{"type":"base64","media_type":"image/png"`) {
		t.Fatalf("tool_result = %s", body)
	}
}

func TestToolResultWithoutImageStaysText(t *testing.T) {
	raw, err := surveyjson.Marshal(ContentBlock{Type: "tool_result", ToolUseID: "call-1", ResultText: "ok"})
	testutil.FailErr(t, "marshal tool result", err)
	if string(raw) != `{"type":"tool_result","tool_use_id":"call-1","content":"ok"}` {
		t.Fatalf("tool_result = %s", raw)
	}
}
