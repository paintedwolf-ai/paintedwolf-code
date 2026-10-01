package ollama

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestToolImagesRideTheNextUserMessage(t *testing.T) {
	png := tinyPNG(t)
	out := ProjectMessages([]api.Message{
		{Role: api.MessageRoleUser, Content: "check"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{{ID: "call-1", Name: "capture_page"}}},
		{Role: api.MessageRoleTool, Content: "captured", ToolResult: &api.ToolResult{
			ToolCallID: "call-1", Tool: "capture_page", Content: "captured",
			Visual: &api.VisualArtifact{ID: "art-1", Mime: "image/png", Bytes: png, Perceive: true},
		}},
	}, true, "sess-1")
	if len(out) != 4 {
		t.Fatalf("messages = %+v", out)
	}
	tool, images := out[2], out[3]
	if tool.Role != "tool" || len(tool.Images) != 0 {
		t.Fatalf("tool message = %+v", tool)
	}
	if images.Role != "user" || len(images.Images) != 1 || !strings.Contains(images.Content, "⟦D:tool:image⟧") {
		t.Fatalf("image message = %+v", images)
	}
}
