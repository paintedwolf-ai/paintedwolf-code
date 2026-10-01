package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/oar"
)

// MCPRuntimeView provides session MCP state.
type MCPRuntimeView interface {
	oar.MCPCatalogView
	ToolLoadingModes(ctx context.Context, projectDir string) map[string]bool
}

// SetOARPipeline connects policy evaluation, rendering, and advisory delivery.
func (m *Manager) SetOARPipeline(p *oar.GuardPipeline, r *oar.Renderer) {
	if m == nil {
		return
	}
	m.oarPipeline = p
	m.oarRenderer = r
	if p != nil {
		p.SetAdvisorySink(m.deliverPolicyAdvisories)
	}
}

// SetMCPRuntime installs session MCP state.
func (m *Manager) SetMCPRuntime(runtime MCPRuntimeView) {
	if m == nil {
		return
	}
	m.mcpRuntime = runtime
}

// OARPipeline returns the installed guard pipeline (nil when unset).
func (m *Manager) OARPipeline() *oar.GuardPipeline {
	if m == nil {
		return nil
	}
	return m.oarPipeline
}

// OARRenderer returns the installed decision renderer (nil when unset).
func (m *Manager) OARRenderer() *oar.Renderer {
	if m == nil {
		return nil
	}
	return m.oarRenderer
}
