// Package ingestion identifies tool outputs from external sources.
package ingestion

import "strings"

// Wire names of the host tools that retrieve external content.
const (
	// ToolWebSearch also crawls discovered content hosts in direct mode.
	ToolWebSearch = "web_search"
	// ToolFetchURL follows redirects.
	ToolFetchURL = "fetch_url"
)

// MCPToolPrefix marks tool names registered from external providers.
const MCPToolPrefix = "mcp_"

// IsRetrievalTool identifies external content producers.
func IsRetrievalTool(toolName string) bool {
	name := normalize(toolName)
	return name == ToolWebSearch || name == ToolFetchURL || strings.HasPrefix(name, MCPToolPrefix)
}

// IsMCPToolName reports whether toolName is a qualified MCP registry tool.
func IsMCPToolName(toolName string) bool {
	return strings.HasPrefix(normalize(toolName), MCPToolPrefix)
}

func normalize(toolName string) string {
	return strings.TrimSpace(strings.ToLower(toolName))
}
