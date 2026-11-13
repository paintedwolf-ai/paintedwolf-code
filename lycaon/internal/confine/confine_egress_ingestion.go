package confine

import (
	"strings"

	"github.com/lycaon/lycaon/internal/ingestion"
)

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

// OriginOf reports how the request came by host. A host the agent named, or
// one beneath it, is authored whatever the rest of the request reaches.
func (c EgressCommand) OriginOf(host string) AddressOrigin {
	host = strings.ToLower(strings.TrimSpace(host))
	for _, named := range c.NamedHosts {
		if host == named || strings.HasSuffix(host, "."+named) {
			return AddressAuthored
		}
	}
	return c.Origin
}

// RetrievalDial reports a retrieval capability, such as web research or an
// MCP tool, reaching one of its sources at host.
func RetrievalDial(cmd EgressCommand, host string) bool {
	return cmd.OriginOf(host) == AddressSource && retrievalTool(cmd)
}

// DisplayedLinkDial reports a retrieval capability following a link a read
// page displayed. The agent chose it, so the gate weighs it.
func DisplayedLinkDial(cmd EgressCommand, host string) bool {
	return cmd.OriginOf(host) == AddressDisplayed && retrievalTool(cmd)
}

func retrievalTool(cmd EgressCommand) bool {
	return ingestion.IsRetrievalTool(cmd.ToolName) ||
		ingestion.IsRetrievalTool(cmd.Image)
}
