package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAnthropicUserImageBlock_visionEmitsBase64Source(t *testing.T) {
	png := tinyPNG(t)
	providerwire.SetVisualBytesResolver(func(sessionID, artifactID string) ([]byte, string, bool) {
		return png, "image/png", true
	})
	t.Cleanup(func() { providerwire.SetVisualBytesResolver(nil) })

	_, out := ProjectMessages([]api.Message{{
		Role:        api.MessageRoleUser,
		Content:     "describe",
		ArtifactIDs: []string{"art-1"},
	}}, nil, true, "sess-1", "", "")
	if len(out) != 1 {
		t.Fatalf("messages = %d want 1", len(out))
	}
	blocks := out[0].Content
	if len(blocks) != 2 || blocks[0].Type != "text" || blocks[1].Type != "image" {
		t.Fatalf("blocks = %+v", blocks)
	}
	src := blocks[1].Source
	if src == nil || src.Type != "base64" || src.MediaType != "image/png" || src.Data == "" {
		t.Fatalf("image source = %+v", src)
	}
	raw, _ := json.Marshal(blocks[1])
	if !strings.Contains(string(raw), `"type":"image"`) || !strings.Contains(string(raw), `"media_type":"image/png"`) {
		t.Fatalf("marshaled image block = %s", raw)
	}
}

func TestAnthropicUserImageBlock_nonVisionStaysTextOnly(t *testing.T) {
	png := tinyPNG(t)
	providerwire.SetVisualBytesResolver(func(sessionID, artifactID string) ([]byte, string, bool) {
		return png, "image/png", true
	})
	t.Cleanup(func() { providerwire.SetVisualBytesResolver(nil) })

	_, out := ProjectMessages([]api.Message{{
		Role:        api.MessageRoleUser,
		Content:     "describe",
		ArtifactIDs: []string{"art-1"},
	}}, nil, false, "sess-1", "", "")
	if len(out) != 1 || len(out[0].Content) != 1 || out[0].Content[0].Type != "text" {
		t.Fatalf("non-vision turn = %+v", out)
	}
	for _, b := range out[0].Content {
		if b.Type == "image" {
			t.Fatalf("non-vision must not emit image block: %+v", b)
		}
	}
}

func TestAnthropicUserImageBlock_textOnlyUnchanged(t *testing.T) {
	_, out := ProjectMessages([]api.Message{{
		Role:    api.MessageRoleUser,
		Content: "hello",
	}}, nil, true, "sess-1", "", "")
	if len(out) != 1 || len(out[0].Content) != 1 || out[0].Content[0].Type != "text" || out[0].Content[0].Text != "hello" {
		t.Fatalf("text-only turn = %+v", out)
	}
}

func TestAnthropicBuildRequest_visionGatesImageBlocks(t *testing.T) {
	png := tinyPNG(t)
	providerwire.SetVisualBytesResolver(func(sessionID, artifactID string) ([]byte, string, bool) {
		return png, "image/png", true
	})
	t.Cleanup(func() { providerwire.SetVisualBytesResolver(nil) })

	vision := New("anthropic", "https://api.anthropic.com/v1", "k", []modelinfo.Entry{
		{ID: "claude-vision", Capabilities: modelinfo.ModelCapabilities{
			Vision: modelinfo.Evidence(modelinfo.CapabilitySupported, "test"),
		}},
	})
	blind := New("anthropic", "https://api.anthropic.com/v1", "k", []modelinfo.Entry{
		{ID: "claude-text", Capabilities: modelinfo.ModelCapabilities{
			Vision: modelinfo.Evidence(modelinfo.CapabilityUnsupported, "test"),
		}},
	})
	req := modelcall.CompletionRequest{
		Messages: []api.Message{{
			Role:        api.MessageRoleUser,
			Content:     "look",
			ArtifactIDs: []string{"art-1"},
		}},
		Debug: modelcall.RequestDebug{SessionID: "sess-1"},
	}

	gotVision := vision.Prepare(req, false)
	if len(gotVision.Messages) != 1 {
		t.Fatalf("vision messages = %+v", gotVision.Messages)
	}
	hasImage := false
	for _, b := range gotVision.Messages[0].Content {
		if b.Type == "image" {
			hasImage = true
		}
	}
	if !hasImage {
		t.Fatalf("vision buildRequest missing image: %+v", gotVision.Messages[0].Content)
	}

	req.Model = "claude-text"
	gotBlind := blind.Prepare(req, false)
	for _, b := range gotBlind.Messages[0].Content {
		if b.Type == "image" {
			t.Fatalf("non-vision buildRequest leaked image: %+v", b)
		}
	}
}
