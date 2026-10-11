package mcp

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/secretmatch"
	"github.com/lycaon/lycaon/internal/toolcontract"
	"github.com/lycaon/lycaon/internal/tools"
)

// perProviderSyncTimeout bounds one provider's discovery.
const perProviderSyncTimeout = 10 * time.Second

// syncDiscoveryParallelism bounds concurrent discovery.
const syncDiscoveryParallelism = 4

// SyncTools refreshes dynamic tool definitions.
// Provider failures remain isolated and appear in status.
func (r *ToolDiscovery) SyncTools(ctx context.Context) error {
	r.syncMu.Lock()
	defer r.syncMu.Unlock()
	return r.syncToolsLocked(ctx)
}

// providerDiscovery is one provider's contribution to a sync.
type providerDiscovery struct {
	entry MCPProviderEntry
	tools []*sdkmcp.Tool
	stage string
	err   error
}

func (r *ToolDiscovery) syncToolsLocked(ctx context.Context) error {
	r.mu.RLock()
	reg := r.toolRegistry
	catalog := append([]MergedMCPProviderEntry(nil), r.Catalog.deviceCatalog...)
	r.mu.RUnlock()
	if reg == nil {
		return nil
	}

	enabled := make([]MCPProviderEntry, 0, len(catalog))
	for _, merged := range catalog {
		if merged.Enabled {
			enabled = append(enabled, merged.MCPProviderEntry)
			continue
		}
		r.Connections.closeProviderSessions(merged.ID)
	}

	discoveries := r.discoverAll(ctx, enabled)

	definitions := make([]tools.Definition, 0)
	declarations := make([]evidence.MCPToolDeclaration, 0)
	toolRefs := map[string]registeredTool{}
	toolDefs := map[string][]sanitizedToolDefinition{}
	syncErrors := map[string]string{}
	syncOK := map[string]bool{}

	for _, d := range discoveries {
		if d.err != nil {
			logSyncFailure(d.entry.ID, d.stage, d.err)
			syncErrors[d.entry.ID] = SyncFailureCode(d.err)
			r.Connections.closeProviderSessions(d.entry.ID)
			continue
		}
		providerDefinitions := make([]tools.Definition, 0, len(d.tools))
		providerDeclarations := make([]evidence.MCPToolDeclaration, 0, len(d.tools))
		providerRefs := map[string]registeredTool{}
		providerDefs := make([]sanitizedToolDefinition, 0, len(d.tools))
		registered := true
		for _, tool := range d.tools {
			def, sanitized, declaration, err := r.buildToolDefinition(d.entry, tool)
			if err != nil {
				logSyncFailure(d.entry.ID, "register_tool", err)
				syncErrors[d.entry.ID] = SyncFailureCode(err)
				registered = false
				break
			}
			if def.Handler == nil {
				continue
			}
			if _, duplicate := providerRefs[def.Meta.Name]; duplicate {
				err := fmt.Errorf("duplicate tool definition %q", def.Meta.Name)
				logSyncFailure(d.entry.ID, "register_tool", err)
				syncErrors[d.entry.ID] = SyncFailureCode(err)
				registered = false
				break
			}
			providerDefinitions = append(providerDefinitions, def)
			providerRefs[def.Meta.Name] = registeredTool{ProviderID: d.entry.ID, ToolName: sanitized.Name}
			providerDefs = append(providerDefs, sanitized)
			if declaration != nil {
				providerDeclarations = append(providerDeclarations, *declaration)
			}
		}
		if registered {
			if err := r.observeProviderToolDefinitions(d.entry.ID, providerDefs); err != nil {
				// Unreadable pin state holds only this provider at its last published generation.
				logSyncFailure(d.entry.ID, "pin_tool_definitions", err)
				syncErrors[d.entry.ID] = CodeToolPinUnreadable
				registered = false
			}
		}
		if registered {
			definitions = append(definitions, providerDefinitions...)
			declarations = append(declarations, providerDeclarations...)
			for name, ref := range providerRefs {
				toolRefs[name] = ref
			}
			toolDefs[d.entry.ID] = providerDefs
			syncOK[d.entry.ID] = true
		}
	}
	validator := tools.NewDefaultRegistry()
	if err := validator.ReplacePrefix("mcp_", definitions, nil); err != nil {
		return err
	}
	if err := reg.ReplacePrefix("mcp_", definitions, func() error {
		r.mu.Lock()
		r.toolRefs = toolRefs
		r.toolDefs = toolDefs
		r.syncErrors = syncErrors
		r.syncOK = syncOK
		r.mu.Unlock()
		return nil
	}); err != nil {
		return err
	}
	// Publish evidence after registry updates so citations refer to callable tools.
	return evidence.ActiveBinding().ReplaceMCPToolDeclarations(declarations)
}

// discoverAll lists tools in device scope with bounded concurrency.
func (r *ToolDiscovery) discoverAll(ctx context.Context, entries []MCPProviderEntry) []providerDiscovery {
	out := make([]providerDiscovery, len(entries))
	sem := make(chan struct{}, syncDiscoveryParallelism)
	var wg sync.WaitGroup
	for i, entry := range entries {
		wg.Add(1)
		go func(i int, entry MCPProviderEntry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[i] = r.discoverOne(ctx, entry)
		}(i, entry)
	}
	wg.Wait()
	return out
}

func (r *ToolDiscovery) discoverOne(ctx context.Context, entry MCPProviderEntry) providerDiscovery {
	providerCtx, cancel := context.WithTimeout(ctx, perProviderSyncTimeout)
	defer cancel()
	scope := CallScope{}
	sess, err := r.Connections.ensureSession(providerCtx, scope, entry)
	if err != nil {
		return providerDiscovery{entry: entry, stage: "connect", err: err}
	}
	list, err := sess.ListTools(providerCtx)
	if err != nil {
		r.Connections.evictDeadSession(scope, entry.ID, err)
		return providerDiscovery{entry: entry, stage: "list_tools", err: err}
	}
	return providerDiscovery{entry: entry, tools: list}
}

func logSyncFailure(providerID, stage string, err error) {
	slog.Warn("mcp provider unavailable; continuing without it",
		"provider_id", providerID, "stage", stage, "error", err)
}

func (r *ToolDiscovery) buildToolDefinition(entry MCPProviderEntry, tool *sdkmcp.Tool) (tools.Definition, sanitizedToolDefinition, *evidence.MCPToolDeclaration, error) {
	if tool == nil {
		return tools.Definition{}, sanitizedToolDefinition{}, nil, nil
	}
	providerID := entry.ID
	def := sanitizeToolDefinition(tool)
	qualified := QualifiedToolName(providerID, def.Name)
	declaration, err := EvidenceDeclarationFromTool(qualified, tool)
	if err != nil {
		return tools.Definition{}, sanitizedToolDefinition{}, nil, err
	}
	providerIDCopy := providerID
	toolNameCopy := def.Name
	handler := func(ctx context.Context, args map[string]any, tctx tools.ToolContext) (string, error) {
		ctx = secretmatch.WithAskAttribution(ctx, secretmatch.AskAttribution{
			SessionID:     tctx.Identity.SessionID,
			RootSessionID: tctx.ChatSessionID(),
			ProjectID:     tctx.Identity.ProjectID,
			ProjectDir:    tctx.ActiveRootPath(),
			ToolCallID:    tctx.Identity.ToolCallID,
		})
		// Invocation scope selects the project overlay and confinement roots.
		return r.Calls.CallTool(ctx, ScopeFromToolContext(tctx), providerIDCopy, toolNameCopy, cloneToolArgs(args))
	}
	contract := toolcontract.External("mcp:" + providerID)
	contract.SecretReferenceSurface = toolcontract.SecretSurfaceMCP
	definition := tools.Definition{
		Meta: tools.ToolMeta{
			Name: qualified, Description: def.Description, ArgsSchema: def.Schema,
			ApprovalCategory: "mcp", ApprovalSubject: providerID + "." + def.Name,
			UntrustedMetadata: true, ReadOnlyHint: def.ReadOnly,
			Source: tools.ToolSourceMCP, SourceID: providerID, AlwaysLoad: entry.AlwaysLoadsTools(),
		},
		Contract: contract,
		Handler:  handler,
	}
	return definition, def, declaration, nil
}

func mcpReadOnlyHint(tool *sdkmcp.Tool) bool {
	if tool == nil || tool.Annotations == nil {
		return false
	}
	return tool.Annotations.ReadOnlyHint
}

func cloneToolArgs(args map[string]any) map[string]any {
	if args == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = v
	}
	return out
}

// Resync closes every MCP session and re-registers tools.
func (r *ToolDiscovery) Resync(ctx context.Context) error {
	r.mu.RLock()
	refs := make([]sessionRef, 0, len(r.Connections.sessions))
	for ref := range r.Connections.sessions {
		refs = append(refs, ref)
	}
	r.mu.RUnlock()
	for _, ref := range refs {
		r.Connections.closeSessionRef(ref)
	}
	return r.SyncTools(ctx)
}
