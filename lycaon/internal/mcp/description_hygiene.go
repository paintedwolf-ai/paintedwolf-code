package mcp

import (
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lycaon/lycaon/internal/textguard"
)

// sanitizeToolSchema cleans string values without changing wire keys.
func sanitizeToolSchema(schema map[string]any) map[string]any {
	cleaned, _ := sanitizeSchemaValue(schema).(map[string]any)
	if cleaned == nil {
		return map[string]any{"type": "object"}
	}
	return cleaned
}

func sanitizeSchemaValue(v any) any {
	switch t := v.(type) {
	case string:
		return textguard.StripInvisibleFormatRunesUntilStable(t)
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			out[k] = sanitizeSchemaValue(val)
		}
		return out
	case []any:
		out := make([]any, 0, len(t))
		for _, e := range t {
			out = append(out, sanitizeSchemaValue(e))
		}
		return out
	default:
		return v
	}
}

// sanitizedToolDefinition is the shared discovery projection.
type sanitizedToolDefinition struct {
	Name        string
	Title       string
	Description string
	Schema      map[string]any
	ReadOnly    bool
}

func sanitizeToolDefinition(tool *sdkmcp.Tool) sanitizedToolDefinition {
	if tool == nil {
		return sanitizedToolDefinition{Schema: map[string]any{"type": "object"}}
	}
	return sanitizedToolDefinition{
		Name:        tool.Name,
		Title:       textguard.StripInvisibleFormatRunesUntilStable(toolDisplayTitle(tool)),
		Description: textguard.StripInvisibleFormatRunesUntilStable(tool.Description),
		Schema:      sanitizeToolSchema(mcpToolInputSchema(tool)),
		ReadOnly:    mcpReadOnlyHint(tool),
	}
}

// toolDisplayTitle applies protocol title precedence.
func toolDisplayTitle(tool *sdkmcp.Tool) string {
	if tool == nil {
		return ""
	}
	if tool.Title != "" {
		return tool.Title
	}
	if tool.Annotations != nil {
		return tool.Annotations.Title
	}
	return ""
}
