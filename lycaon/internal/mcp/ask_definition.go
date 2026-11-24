package mcp

import (
	"context"
	"slices"

	"github.com/lycaon/lycaon/internal/toolrejection"
	"github.com/lycaon/lycaon/internal/tools"
)

func mcpDefinitionUnavailable(providerID, toolName string) *toolrejection.ToolReject {
	return &toolrejection.ToolReject{
		Code: MCPConsentStateUnavailableCode,
		Data: map[string]any{"provider": providerID, "tool": toolName},
	}
}

// Compare the scoped live definition to the registry generation held by dispatch.
func (r *ToolCalls) verifyAskDefinition(ctx context.Context, scope CallScope, entry MCPProviderEntry, name string) (ProviderSession, error) {
	r.Tools.mu.RLock()
	registered := r.Tools.toolDefs[entry.ID]
	index := slices.IndexFunc(registered, func(def sanitizedToolDefinition) bool { return def.Name == name })
	var expected sanitizedToolDefinition
	if index >= 0 {
		expected = registered[index]
	}
	r.Tools.mu.RUnlock()
	if !expected.ReadOnly {
		return nil, tools.AskModeReject(QualifiedToolName(entry.ID, name))
	}
	want, err := toolDefinitionFingerprint(expected)
	if err != nil {
		return nil, mcpDefinitionUnavailable(entry.ID, name)
	}
	session, err := r.Connections.ensureSession(ctx, scope, entry)
	if err != nil {
		return nil, err
	}
	available, err := session.ListTools(ctx)
	if err != nil {
		r.Connections.evictDeadSession(scope, entry.ID, err)
		return nil, err
	}
	for _, tool := range available {
		if tool == nil || tool.Name != name {
			continue
		}
		observed := sanitizeToolDefinition(tool)
		if !observed.ReadOnly {
			return nil, tools.AskModeReject(QualifiedToolName(entry.ID, name))
		}
		got, err := toolDefinitionFingerprint(observed)
		if err != nil || got != want {
			break
		}
		if err := r.Tools.acceptToolDefinition(entry.ID, name); err == nil {
			return session, nil
		}
		break
	}
	return nil, mcpDefinitionUnavailable(entry.ID, name)
}
