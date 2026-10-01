package openaicompat

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type wireMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	ToolCallID string          `json:"tool_call_id"`
}

type wirePart struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url"`
}

func toolImagePNG(t *testing.T) []byte {
	t.Helper()
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==")
	testutil.FailErr(t, "decode fixture png", err)
	return png
}

// twoCaptureTurn is one assistant turn with two parallel captures, both
// stored as artifacts.
func twoCaptureTurn() []api.Message {
	result := func(callID, artifactID string) api.Message {
		return api.Message{Role: api.MessageRoleTool, Content: `{"ok":true}`, ToolResult: &api.ToolResult{
			ToolCallID: callID, Tool: "capture_page", Content: `{"ok":true}`,
			Visual: &api.VisualArtifact{ID: artifactID, Mime: "image/png", StoreRef: true, Perceive: true},
		}}
	}
	return []api.Message{
		{Role: api.MessageRoleUser, Content: "check the page"},
		{Role: api.MessageRoleAssistant, ToolCalls: []api.ToolCall{
			{ID: "call-a", Name: "capture_page", Args: map[string]any{}},
			{ID: "call-b", Name: "capture_page", Args: map[string]any{}},
		}},
		result("call-a", "art-a"),
		result("call-b", "art-b"),
		{Role: api.MessageRoleUser, Content: "next"},
	}
}

func encodeToolImageTurn(t *testing.T, profile providerprofile.Profile) []wireMessage {
	t.Helper()
	png := toolImagePNG(t)
	providerwire.SetVisualBytesResolver(func(string, string) ([]byte, string, bool) { return png, "image/png", true })
	t.Cleanup(func() { providerwire.SetVisualBytesResolver(nil) })
	provider := New("vision", "https://example.invalid/v1", "key", []modelinfo.Entry{{
		ID: "vision-model", Capabilities: modelinfo.ModelCapabilities{
			Vision: modelinfo.CapabilityEvidence{State: modelinfo.CapabilitySupported},
		},
	}}).WithProfile(profile)
	body, err := encodeChatCompletionRequest(modelcall.CompletionRequest{
		Model: "vision-model", Messages: twoCaptureTurn(), Debug: modelcall.RequestDebug{SessionID: "sess-1"},
	}, provider, false, controlOpts{})
	testutil.FailErr(t, "encode request", err)
	var wire struct {
		Messages []wireMessage `json:"messages"`
	}
	testutil.FailErr(t, "decode request", json.Unmarshal(body, &wire))
	return wire.Messages
}

func contentParts(t *testing.T, m wireMessage) []wirePart {
	t.Helper()
	var parts []wirePart
	if err := json.Unmarshal(m.Content, &parts); err != nil {
		return nil
	}
	return parts
}

func TestToolImagesFollowTheirRunInOneUserMessage(t *testing.T) {
	msgs := encodeToolImageTurn(t, providerprofile.OpenAI())
	roles := make([]string, len(msgs))
	for i, m := range msgs {
		roles[i] = m.Role
	}
	// The images wait until both tool results have answered their calls.
	if got := strings.Join(roles, ","); got != "user,assistant,tool,tool,user,user" {
		t.Fatalf("roles = %s", got)
	}
	for _, m := range msgs[2:4] {
		var text string
		if err := json.Unmarshal(m.Content, &text); err != nil {
			t.Fatalf("tool message %s content must stay text: %s", m.ToolCallID, m.Content)
		}
		if !strings.Contains(text, `"image":"attached"`) {
			t.Fatalf("tool message lacks the attached fact: %q", text)
		}
	}
	parts := contentParts(t, msgs[4])
	if len(parts) != 4 {
		t.Fatalf("image message parts = %+v", parts)
	}
	for i, callID := range []string{"call-a", "call-b"} {
		caption, image := parts[2*i], parts[2*i+1]
		if caption.Type != "text" || !strings.Contains(caption.Text, "⟦D:tool:image⟧") || !strings.Contains(caption.Text, callID) {
			t.Fatalf("caption %d = %+v", i, caption)
		}
		if image.Type != "image_url" || image.ImageURL == nil || !strings.HasPrefix(image.ImageURL.URL, "data:image/png;base64,") {
			t.Fatalf("image %d = %+v", i, image)
		}
	}
}

func TestToolImagesRideInsideToolMessagesWhereAccepted(t *testing.T) {
	msgs := encodeToolImageTurn(t, providerprofile.Fireworks())
	if len(msgs) != 5 {
		t.Fatalf("messages = %d, want no extra image message", len(msgs))
	}
	for _, m := range msgs[2:4] {
		parts := contentParts(t, m)
		if m.Role != "tool" || len(parts) != 2 || parts[0].Type != "text" || parts[1].Type != "image_url" {
			t.Fatalf("tool message %s = %s", m.ToolCallID, m.Content)
		}
	}
}
