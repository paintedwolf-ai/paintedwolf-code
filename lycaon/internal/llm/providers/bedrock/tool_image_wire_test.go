package bedrock

import (
	"testing"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

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
	}, nil, true, "sess-1")
	last := out[len(out)-1]
	result, ok := last.Content[0].(*brtypes.ContentBlockMemberToolResult)
	if !ok || len(result.Value.Content) != 2 {
		t.Fatalf("tool result = %+v", last.Content)
	}
	image, ok := result.Value.Content[1].(*brtypes.ToolResultContentBlockMemberImage)
	if !ok || image.Value.Format != brtypes.ImageFormatPng {
		t.Fatalf("image block = %+v", result.Value.Content[1])
	}
}
