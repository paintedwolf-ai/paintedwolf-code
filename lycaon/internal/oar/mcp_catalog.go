package oar

import (
	"errors"
	"strings"
)

// MCPCatalogView supplies provider and tool identity facts.
type MCPCatalogView interface {
	ProviderConfigured(id string) bool
	ProviderEnabled(id string) bool
	ConfiguredProviderIDs() []string
	EnabledProviderIDs() []string
	ResolveQualifiedTool(qualified string) (providerID, toolName string, ok bool)
}

// ObserveMCPStructuralPre fills the structural MCP facts for tool.pre_invoke.
// Non-mcp_* tools leave all MCP facts at zero.
func ObserveMCPStructuralPre(gc *GuardContext, tool string, catalog MCPCatalogView) {
	if gc == nil {
		return
	}
	gc.ClearMCPObservation()
	gc.MCP.MCPCatalog = catalog
	if !strings.HasPrefix(tool, "mcp_") {
		return
	}
	gc.MCP.MCPQualifiedTool = tool
	gc.MCP.MCPCallOK = false
	gc.MCP.MCPErrorCode = ""
	gc.MCP.MCPSchemaMatched = false
	if catalog == nil {
		return
	}
	providerID, toolName, ok := catalog.ResolveQualifiedTool(tool)
	if !ok {
		return
	}
	gc.MCP.MCPServerID = providerID
	gc.MCP.MCPToolName = toolName
	gc.MCP.MCPServerConfigured = catalog.ProviderConfigured(providerID)
	gc.MCP.MCPServerEnabled = catalog.ProviderEnabled(providerID)
}

// ObserveMCPStructuralPost fills the structural MCP facts for tool.post_invoke.
// callOK and errorCode carry structured protocol outcomes.
// resultText is the CallTool body for lazy schema binding.
func ObserveMCPStructuralPost(gc *GuardContext, tool string, catalog MCPCatalogView, callOK bool, errorCode, resultText string) {
	ObserveMCPStructuralPre(gc, tool, catalog)
	if gc == nil || !strings.HasPrefix(tool, "mcp_") {
		return
	}
	gc.MCP.MCPCallOK = callOK
	gc.MCP.MCPErrorCode = errorCode
	gc.MCP.MCPResultText = resultText
	gc.MCP.MCPSchemaMatched = false
	gc.MCP.MCPFields = nil
}

// configuredServers is mcp_configured_servers: every catalogue server id.
func (m *MCPFacts) configuredServers() []string {
	if m == nil || m.MCPCatalog == nil {
		return []string{}
	}
	return strList(m.MCPCatalog.ConfiguredProviderIDs())
}

// enabledServers is mcp_enabled_servers: every enabled catalogue server id.
func (m *MCPFacts) enabledServers() []string {
	if m == nil || m.MCPCatalog == nil {
		return []string{}
	}
	return strList(m.MCPCatalog.EnabledProviderIDs())
}

// MCPMachineErrorCode returns the declared protocol code, or an empty string.
func MCPMachineErrorCode(err error) string {
	if err == nil {
		return ""
	}
	type coder interface {
		MachineErrorCode() string
	}
	var c coder
	if errors.As(err, &c) && c != nil {
		return c.MachineErrorCode()
	}
	return ""
}
