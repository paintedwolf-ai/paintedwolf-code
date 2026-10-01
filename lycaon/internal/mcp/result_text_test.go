package mcp

import (
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestExtractTextBlocksJoined(t *testing.T) {
	res := &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
		&sdkmcp.TextContent{Text: "first"},
		&sdkmcp.TextContent{Text: "second"},
	}}
	if got := ExtractToolResultText(res); got != "first\nsecond" {
		t.Fatalf("text = %q", got)
	}
}

// A tool that declares an outputSchema may answer with structuredContent alone; the
// model must still receive that content rather than an empty string.
func TestStructuredOnlyResultIsRendered(t *testing.T) {
	res := &sdkmcp.CallToolResult{
		StructuredContent: map[string]any{"rows": []any{float64(1), float64(2)}},
	}
	got := ExtractToolResultText(res)
	if got == "" {
		t.Fatal("structured-only result rendered as empty")
	}
	if !strings.Contains(got, `"rows"`) {
		t.Fatalf("structured payload missing from %q", got)
	}
}

// Text wins when both are present: the server serialized its own copy, and re-appending
// the structured tree would duplicate the whole payload in the transcript.
func TestTextPreferredOverStructured(t *testing.T) {
	res := &sdkmcp.CallToolResult{
		Content:           []sdkmcp.Content{&sdkmcp.TextContent{Text: "rendered"}},
		StructuredContent: map[string]any{"rows": []any{float64(1)}},
	}
	if got := ExtractToolResultText(res); got != "rendered" {
		t.Fatalf("text = %q", got)
	}
}

// Binary and referenced content carries no text to inline, but dropping it silently
// reads to the model as a tool that returned nothing.
func TestNonTextContentIsAccountedFor(t *testing.T) {
	res := &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
		&sdkmcp.ImageContent{Data: []byte("x"), MIMEType: "image/png"},
		&sdkmcp.ResourceLink{URI: "file:///tmp/report.pdf", MIMEType: "application/pdf"},
	}}
	got := ExtractToolResultText(res)
	for _, want := range []string{"image/png", "file:///tmp/report.pdf", "not inlined"} {
		if !strings.Contains(got, want) {
			t.Fatalf("result %q missing %q", got, want)
		}
	}
	if strings.Contains(got, "x") && strings.Contains(got, "data:") {
		t.Fatalf("image bytes leaked into the transcript: %q", got)
	}
}

func TestEmptyResultStaysEmpty(t *testing.T) {
	if got := ExtractToolResultText(&sdkmcp.CallToolResult{}); got != "" {
		t.Fatalf("empty result = %q", got)
	}
	if got := ExtractToolResultText(nil); got != "" {
		t.Fatalf("nil result = %q", got)
	}
}
