package bedrock

import (
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestBedrockBuildConverseInputStripsPerceiveImages(t *testing.T) {
	png := tinyPNG(t)
	p := New("bedrock", "us-east-1", "", []modelinfo.Entry{{ID: "amazon.nova-pro-v1:0"}})
	req := modelcall.CompletionRequest{
		Model: "amazon.nova-pro-v1:0",
		Messages: []api.Message{{
			Role:    api.MessageRoleUser,
			Content: "describe",
			ToolResult: &api.ToolResult{
				Content: "shot",
				Visual: &api.VisualArtifact{
					Perceive: true,
					Bytes:    png,
					Mime:     "image/png",
				},
			},
		}},
	}
	// The same strip buildConverseInput applies.
	stripped := providerwire.PrepareMessagesForVision(req.Messages, false, req.Debug.SessionID)
	if len(stripped) != 1 || stripped[0].ToolResult == nil || stripped[0].ToolResult.Visual == nil {
		t.Fatalf("stripped = %+v", stripped)
	}
	vis := stripped[0].ToolResult.Visual
	if vis.Perceive || len(vis.Bytes) != 0 {
		t.Fatalf("perceive must be cleared before Bedrock wire: %+v", vis)
	}
	_ = p.buildConverseInput(req, req.Model)
}
