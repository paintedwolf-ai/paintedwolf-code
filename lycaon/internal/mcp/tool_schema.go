package mcp

import sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

func mcpToolInputSchema(tool *sdkmcp.Tool) map[string]any {
	if tool == nil || tool.InputSchema == nil {
		return map[string]any{"type": "object"}
	}
	if schema, ok := tool.InputSchema.(map[string]any); ok && len(schema) > 0 {
		return schema
	}
	return map[string]any{"type": "object"}
}
