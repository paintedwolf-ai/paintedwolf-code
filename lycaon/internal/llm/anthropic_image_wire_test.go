package llm

import (
	"encoding/base64"
	"strings"
	"testing"

	anthropicprovider "github.com/lycaon/lycaon/internal/llm/providers/anthropic"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func tinyPNG(t *testing.T) []byte {
	t.Helper()
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	testutil.FailErr(t, "base64.StdEncoding.DecodeString failed", err)
	return png
}

func TestAnthropicUserImageBlock_parityWithOpenAICompat(t *testing.T) {
	png := tinyPNG(t)
	providerwire.SetVisualBytesResolver(func(sessionID, artifactID string) ([]byte, string, bool) {
		return png, "image/png", true
	})
	t.Cleanup(func() { providerwire.SetVisualBytesResolver(nil) })

	msgs := []api.Message{{
		Role:        api.MessageRoleUser,
		Content:     "what is this?",
		ArtifactIDs: []string{"art-twin"},
	}}

	_, anth := anthropicprovider.ProjectMessages(msgs, nil, true, "sess-1", "", "")
	oai := openaicompat.ProjectMessages(msgs, openaicompat.MessageProjection{Vision: true, SessionID: "sess-1"})

	var anthImage *anthropicprovider.ImageSource
	for _, b := range anth[0].Content {
		if b.Type == "image" {
			anthImage = b.Source
			break
		}
	}
	if anthImage == nil {
		t.Fatal("anthropic wire missing image block")
	}

	parts, ok := oai[0].Content.([]openaicompat.ContentPart)
	if !ok {
		t.Fatalf("openai content type = %T", oai[0].Content)
	}
	var oaiURL string
	for _, p := range parts {
		if p.Type == "image_url" && p.ImageURL != nil {
			oaiURL = p.ImageURL.URL
			break
		}
	}
	wantPrefix := "data:" + anthImage.MediaType + ";base64,"
	if !strings.HasPrefix(oaiURL, wantPrefix) {
		t.Fatalf("openai url = %q want prefix %q", oaiURL, wantPrefix)
	}
	if oaiURL[len(wantPrefix):] != anthImage.Data {
		t.Fatalf("base64 payload mismatch anthropic vs openai")
	}
}
