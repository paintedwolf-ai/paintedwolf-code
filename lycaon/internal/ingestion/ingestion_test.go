package ingestion

import "testing"

func TestRetrievalToolsRecognized(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		ToolWebSearch, ToolFetchURL, MCPToolPrefix + "acme_ship",
		// Names arrive from the wire with whatever spacing and casing the caller had.
		"  WEB_SEARCH  ", "MCP_Acme_Ship",
	} {
		if !IsRetrievalTool(name) {
			t.Errorf("%q not recognized as a retrieval tool", name)
		}
	}
}

// Presence here exempts a dial from the ingestion gate and marks the tool's
// output, so anything that acts rather than reads must be absent.
func TestActingToolsAreNotRetrieval(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"command", "verify", "write", "edit", "read", "grep", "git_commit",
		"terminal_open", "render_view", "page_open", "", "   ",
		// Near-misses on the MCP namespace.
		"mcp", "mcpx", "my_mcp_tool",
	} {
		if IsRetrievalTool(name) {
			t.Errorf("%q classifies as retrieval", name)
		}
	}
}

func TestIsMCPToolNameMatchesTheNamespaceOnly(t *testing.T) {
	t.Parallel()
	if !IsMCPToolName(MCPToolPrefix + "anything") {
		t.Error("qualified MCP tool not recognized")
	}
	for _, name := range []string{"mcp", "mcpx", "web_search", ""} {
		if IsMCPToolName(name) {
			t.Errorf("%q matched the MCP namespace", name)
		}
	}
}
