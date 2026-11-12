package confine

import "github.com/lycaon/lycaon/internal/ingestion"

// AddressOrigin is how a retrieval tool came by the address it requests.
type AddressOrigin uint8

const (
	// AddressSource is one of the tool's sources: an endpoint it declared, or
	// an address the chat's evidence records as returned or read.
	AddressSource AddressOrigin = iota
	// AddressDisplayed is a link a read page displayed.
	AddressDisplayed
	// AddressAuthored is an address the agent wrote.
	AddressAuthored
)

// RetrievalDial reports a retrieval capability, such as web research or an
// MCP tool, reaching one of its sources.
func RetrievalDial(cmd EgressCommand) bool {
	return cmd.Origin == AddressSource && retrievalTool(cmd)
}

// DisplayedLinkDial reports a retrieval capability following a link a read
// page displayed. The agent chose it, so the gate weighs it.
func DisplayedLinkDial(cmd EgressCommand) bool {
	return cmd.Origin == AddressDisplayed && retrievalTool(cmd)
}

func retrievalTool(cmd EgressCommand) bool {
	return ingestion.IsRetrievalTool(cmd.ToolName) ||
		ingestion.IsRetrievalTool(cmd.Image)
}
