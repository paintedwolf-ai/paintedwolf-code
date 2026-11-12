package confine

import "github.com/lycaon/lycaon/internal/ingestion"

// RetrievalDial reports a dial made by a declared retrieval capability, such
// as web research or an MCP tool, whose contract covers reaching its sources.
func RetrievalDial(cmd EgressCommand) bool {
	return ingestion.IsRetrievalTool(cmd.ToolName) ||
		ingestion.IsRetrievalTool(cmd.Image)
}
