package mcp

import (
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lycaon/lycaon/pkg/api"
)

func TestBuildToolDefinitionCarriesProviderLoadingMode(t *testing.T) {
	reg := &RegistryImpl{}
	def, _, _, err := reg.buildToolDefinition(MCPProviderEntry{
		ID: "tracker", ToolLoading: api.McpToolLoadingAlways,
	}, &sdkmcp.Tool{Name: "search", InputSchema: map[string]any{"type": "object"}})
	if err != nil {
		t.Fatalf("buildToolDefinition: %v", err)
	}
	if !def.Meta.AlwaysLoad {
		t.Fatal("always provider metadata must keep its loading mode")
	}
}
