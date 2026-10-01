package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ExtractToolResultText renders an MCP tool result into model-readable text.
func ExtractToolResultText(res *sdkmcp.CallToolResult) string {
	if res == nil {
		return ""
	}
	var parts []string
	var carried []string
	for _, c := range res.Content {
		if tc, ok := c.(*sdkmcp.TextContent); ok {
			if tc.Text != "" {
				parts = append(parts, tc.Text)
			}
			continue
		}
		if note := nonTextContentNote(c); note != "" {
			carried = append(carried, note)
		}
	}
	if len(parts) == 0 {
		if structured := structuredResultJSON(res.StructuredContent); structured != "" {
			parts = append(parts, structured)
		}
	}
	if len(carried) > 0 {
		parts = append(parts, fmt.Sprintf("[non-text content not inlined: %s]", strings.Join(carried, "; ")))
	}
	return strings.Join(parts, "\n")
}

// structuredResultJSON renders structuredContent as compact JSON, or "" when the server
// sent nothing usable. A marshal failure yields "" rather than an error string: the
// caller's contract is the text the model reads, and a Go error message is not it.
func structuredResultJSON(structured any) string {
	if structured == nil {
		return ""
	}
	raw, err := json.Marshal(structured)
	if err != nil {
		return ""
	}
	if s := strings.TrimSpace(string(raw)); s != "" && s != "null" {
		return s
	}
	return ""
}

// nonTextContentNote describes one non-text block by kind and, where the block carries
// them, MIME type and URI. Field reads only — never a guess from the payload.
func nonTextContentNote(c sdkmcp.Content) string {
	switch t := c.(type) {
	case *sdkmcp.ImageContent:
		return describeContent("image", t.MIMEType, "")
	case *sdkmcp.AudioContent:
		return describeContent("audio", t.MIMEType, "")
	case *sdkmcp.ResourceLink:
		return describeContent("resource_link", t.MIMEType, t.URI)
	case *sdkmcp.EmbeddedResource:
		mime, uri := "", ""
		if t.Resource != nil {
			mime, uri = t.Resource.MIMEType, t.Resource.URI
		}
		return describeContent("resource", mime, uri)
	case nil:
		return ""
	default:
		return "content"
	}
}

func describeContent(kind, mime, uri string) string {
	out := kind
	if m := strings.TrimSpace(mime); m != "" {
		out += " " + m
	}
	if u := strings.TrimSpace(uri); u != "" {
		out += " " + u
	}
	return out
}
