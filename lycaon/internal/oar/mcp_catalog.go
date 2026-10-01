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
	gc.MCPCatalog = catalog
	if !strings.HasPrefix(tool, "mcp_") {
		return
	}
	gc.MCPQualifiedTool = tool
	gc.MCPCallOK = false
	gc.MCPErrorCode = ""
	gc.MCPSchemaMatched = false
	if catalog == nil {
		return
	}
	providerID, toolName, ok := catalog.ResolveQualifiedTool(tool)
	if !ok {
		return
	}
	gc.MCPProviderID = providerID
	gc.MCPToolName = toolName
	gc.MCPProviderConfigured = catalog.ProviderConfigured(providerID)
	gc.MCPProviderEnabled = catalog.ProviderEnabled(providerID)
}

// ObserveMCPStructuralPost fills the structural MCP facts for tool.post_invoke.
// callOK and errorCode carry structured protocol outcomes.
// resultText is the CallTool body for lazy schema binding.
func ObserveMCPStructuralPost(gc *GuardContext, tool string, catalog MCPCatalogView, callOK bool, errorCode, resultText string) {
	ObserveMCPStructuralPre(gc, tool, catalog)
	if gc == nil || !strings.HasPrefix(tool, "mcp_") {
		return
	}
	gc.MCPCallOK = callOK
	gc.MCPErrorCode = errorCode
	gc.MCPResultText = resultText
	gc.MCPSchemaMatched = false
	gc.MCPFields = nil
}

// EvalMCPProviderConfigured is the parameterized observation mcp_provider_configured_for(id).
func EvalMCPProviderConfigured(gc *GuardContext, id string) bool {
	if gc == nil || id == "" || gc.MCPCatalog == nil {
		return false
	}
	return gc.MCPCatalog.ProviderConfigured(id)
}

// EvalMCPProviderEnabled is the parameterized observation mcp_provider_enabled_for(id).
func EvalMCPProviderEnabled(gc *GuardContext, id string) bool {
	if gc == nil || id == "" || gc.MCPCatalog == nil {
		return false
	}
	return gc.MCPCatalog.ProviderEnabled(id)
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
