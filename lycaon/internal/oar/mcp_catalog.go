package oar

import (
	"errors"
	"strings"
)

// MCPCatalogView supplies provider and tool identity facts.
type MCPCatalogView interface {
	ProviderConfigured(id string) bool
	ProviderEnabled(id string) bool
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
	gc.MCP.MCPProviderID = providerID
	gc.MCP.MCPToolName = toolName
	gc.MCP.MCPProviderConfigured = catalog.ProviderConfigured(providerID)
	gc.MCP.MCPProviderEnabled = catalog.ProviderEnabled(providerID)
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

// EvalMCPProviderConfigured is the parameterized observation mcp_provider_configured_for(id).
func EvalMCPProviderConfigured(gc *GuardContext, id string) bool {
	if gc == nil || id == "" || gc.MCP.MCPCatalog == nil {
		return false
	}
	return gc.MCP.MCPCatalog.ProviderConfigured(id)
}

// EvalMCPProviderEnabled is the parameterized observation mcp_provider_enabled_for(id).
func EvalMCPProviderEnabled(gc *GuardContext, id string) bool {
	if gc == nil || id == "" || gc.MCP.MCPCatalog == nil {
		return false
	}
	return gc.MCP.MCPCatalog.ProviderEnabled(id)
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
