package toolcontract

// ToolCapabilitySnapshot binds recovery facts to one frozen tool definition.
type ToolCapabilitySnapshot struct {
	Tool         string
	Capabilities Capability
}
