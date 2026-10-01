package mcp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/tools/safecmd"
)

// shapeMCPOutput applies the per-call output cap.
func shapeMCPOutput(providerID, toolName, text string) (string, error) {
	max := int(safecmd.MCPCaps().InputBytes)
	if max <= 0 || len(text) <= max {
		return text, nil
	}
	qualified := QualifiedToolName(providerID, toolName)
	previewCap := safecmd.MCPZoomBytes
	if previewCap <= 0 {
		previewCap = 64 << 10
	}
	preview := text
	if len(preview) > previewCap {
		preview = preview[:previewCap]
	}
	value := map[string]any{
		"truncated":      true,
		"original_bytes": len(text),
		"cap_bytes":      max,
		"preview":        preview,
	}
	var parsed any
	if err := json.Unmarshal([]byte(text), &parsed); err == nil {
		value["json"] = true
		// Keep a compact note rather than re-embedding the full tree.
		value["note"] = "MCP CallTool result exceeded the host output-size cap; preview only."
	} else {
		value["json"] = false
		value["note"] = "MCP CallTool result exceeded the host output-size cap; non-JSON preview only."
	}
	out, err := safecmd.Shape(safecmd.ShapeInput{
		Tool:      qualified,
		Truncated: true,
		Banner:    fmt.Sprintf("MCP result truncated at %d bytes (was %d).", max, len(text)),
		Value:     value,
	})
	if err != nil {
		// Preserve the output cap.
		if len(text) > max {
			return runeclamp.CutBytes(text, max) + runeclamp.TruncatedSuffix, nil
		}
		return text, nil
	}
	return out, nil
}

// MCPServerCodePrefix namespaces a server-declared reject code.
const MCPServerCodePrefix = "MCP_SERVER_"

// GenericMCPRejectCode identifies an unstructured server error.
const GenericMCPRejectCode = "MCP_TOOL_ERROR"

// declaredMCPRejectCode namespaces a structured server code.
func declaredMCPRejectCode(structured any) string {
	code := normalizeMCPCode(extractDeclaredMCPCode(structured))
	if code == "" {
		return GenericMCPRejectCode
	}
	return MCPServerCodePrefix + code
}

// normalizeMCPCode canonicalizes and bounds a server code.
func normalizeMCPCode(code string) string {
	const maxCodeLen = 64
	var b strings.Builder
	for _, r := range strings.TrimSpace(code) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - ('a' - 'A'))
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
		if b.Len() >= maxCodeLen {
			break
		}
	}
	return strings.Trim(b.String(), "_")
}

// extractDeclaredMCPCode reads the structured code field.
func extractDeclaredMCPCode(structured any) string {
	switch t := structured.(type) {
	case map[string]any:
		if c, ok := t["code"].(string); ok {
			return strings.TrimSpace(c)
		}
	case map[string]string:
		return strings.TrimSpace(t["code"])
	}
	return ""
}
