package openaicompat

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestUserMessageWireContent_attachesVisionParts(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	testutil.FailErr(t, "base64.StdEncoding.DecodeString failed", err)
	providerwire.SetVisualBytesResolver(func(sessionID, artifactID string) ([]byte, string, bool) {
		return png, "image/png", true
	})
	t.Cleanup(func() { providerwire.SetVisualBytesResolver(nil) })

	content := userMessageWireContent("describe", []string{"art-1"}, true, "sess-1")
	parts, ok := content.([]ContentPart)
	if !ok {
		t.Fatalf("content type = %T want []contentPartWire", content)
	}
	if len(parts) != 2 || parts[0].Type != "text" || parts[1].Type != "image_url" {
		t.Fatalf("parts = %+v", parts)
	}
	if parts[1].ImageURL == nil || !strings.HasPrefix(parts[1].ImageURL.URL, "data:image/png;base64,") {
		t.Fatalf("image url = %+v", parts[1].ImageURL)
	}

	plain := userMessageWireContent("describe", []string{"art-1"}, false, "sess-1")
	if plain != "describe" {
		t.Fatalf("non-vision should keep text only; got %#v", plain)
	}

	wire := ProjectMessages([]api.Message{{
		Role: api.MessageRoleUser, Content: "hi", ArtifactIDs: []string{"art-1"},
	}}, MessageProjection{Vision: true, SessionID: "sess-1"})
	if len(wire) != 1 {
		t.Fatalf("wire len = %d", len(wire))
	}
}
