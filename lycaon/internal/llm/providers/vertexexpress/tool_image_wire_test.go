package vertexexpress

import (
	"encoding/base64"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestToolImagesFollowEveryFunctionResponseOfTheirTurn(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	testutil.FailErr(t, "decode fixture png", err)
	capture := func(callID string) api.Message {
		return api.Message{Role: api.MessageRoleTool, Content: "captured", ToolResult: &api.ToolResult{
			ToolCallID: callID, Tool: "capture_page", Content: "captured",
			Visual: &api.VisualArtifact{Mime: "image/png", Bytes: png, Perceive: true},
		}}
	}
	_, out := ProjectMessages([]api.Message{
		{Role: api.MessageRoleUser, Content: "check"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{ID: "call-a", Name: "capture_page"}, {ID: "call-b", Name: "capture_page"},
		}},
		capture("call-a"),
		capture("call-b"),
	}, true, "sess-1")
	parts := out[len(out)-1].Parts
	if len(parts) != 4 {
		t.Fatalf("parts = %+v", parts)
	}
	for i, p := range parts {
		isResponse := p.FunctionResponse != nil
		isImage := p.InlineData != nil && p.InlineData.MimeType == "image/png"
		if want := i < 2; isResponse != want || isImage == want {
			t.Fatalf("part %d = %+v; responses come first, then images", i, p)
		}
	}
}
