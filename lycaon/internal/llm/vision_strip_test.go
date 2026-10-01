package llm

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	ollamaprovider "github.com/lycaon/lycaon/internal/llm/providers/ollama"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestOllamaBuildRequestStripsPerceiveImages(t *testing.T) {
	png := tinyPNG(t)
	p := ollamaprovider.New("ollama", "http://127.0.0.1:9/v1", "", []modelinfo.Entry{
		{ID: "llama3.2", ContextLength: 8192},
	})
	req := modelcall.CompletionRequest{
		Model: "llama3.2",
		Messages: []api.Message{{
			Role: api.MessageRoleTool,
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
	payload, _, err := p.Prepare(t.Context(), req, false)
	if err != nil {
		t.Fatalf("buildRequest: %v", err)
	}
	if len(payload.Messages) != 1 {
		t.Fatalf("messages = %+v", payload.Messages)
	}
	// Native Ollama content is string-only; stripped perceive leaves the text
	// and states why no image followed.
	content := payload.Messages[0].Content
	if !strings.HasPrefix(content, "shot") || !strings.Contains(content, `"reason":"model_lacks_vision"`) {
		t.Fatalf("content = %q", content)
	}
}
