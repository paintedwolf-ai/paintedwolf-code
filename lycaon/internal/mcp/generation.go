package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sort"

	"github.com/lycaon/lycaon/internal/jsonvalue"
	"github.com/lycaon/lycaon/pkg/api"
)

// ResourceGeneration is one detached MCP snapshot for a scope.
type ResourceGeneration struct {
	// Revision is the domain-separated SHA-256 identity of the snapshot.
	Revision string
	// Providers is sorted by ID.
	Providers []GenerationProvider
	// Rejected carries device and project rejected rows.
	Rejected []RejectedRow
}

// GenerationProvider is one provider's captured state.
type GenerationProvider struct {
	ID      string
	Enabled bool
	Status  api.McpProviderStatus
	// Tools is the sorted definition set from the last sync.
	Tools []GenerationTool
}

// GenerationTool is one sanitized tool definition and its fingerprint.
type GenerationTool struct {
	Name        string
	Title       string
	Description string
	Schema      map[string]any
	ReadOnly    bool
	Fingerprint string
}

// Provider returns the captured provider by id.
func (g *ResourceGeneration) Provider(id string) (GenerationProvider, bool) {
	if g == nil {
		return GenerationProvider{}, false
	}
	for _, s := range g.Providers {
		if s.ID == id {
			return s, true
		}
	}
	return GenerationProvider{}, false
}

// Tool returns one captured tool definition by provider and tool name.
func (g *ResourceGeneration) Tool(providerID, name string) (GenerationTool, bool) {
	s, ok := g.Provider(providerID)
	if !ok {
		return GenerationTool{}, false
	}
	for _, t := range s.Tools {
		if t.Name == name {
			return t, true
		}
	}
	return GenerationTool{}, false
}

// CurrentGeneration gives unchanged scoped state a stable revision.
func (r *ProviderCatalog) CurrentGeneration(ctx context.Context, scope CallScope) *ResourceGeneration {
	if r == nil {
		return finishGeneration(&ResourceGeneration{})
	}
	view := r.projectView(ctx, scope.ProjectDir)

	r.mu.RLock()
	syncErrors := make(map[string]string, len(r.Tools.syncErrors))
	for id, msg := range r.Tools.syncErrors {
		syncErrors[id] = msg
	}
	syncOK := make(map[string]bool, len(r.Tools.syncOK))
	for id, ok := range r.Tools.syncOK {
		syncOK[id] = ok
	}
	authRequired := make(map[string]bool, len(r.Credentials.authRequired))
	for id, needed := range r.Credentials.authRequired {
		authRequired[id] = needed
	}
	toolDefs := make(map[string][]sanitizedToolDefinition, len(r.Tools.toolDefs))
	for id, defs := range r.Tools.toolDefs {
		toolDefs[id] = defs
	}
	r.mu.RUnlock()

	gen := &ResourceGeneration{
		Providers: make([]GenerationProvider, 0, len(view.catalog)),
		Rejected:  append([]RejectedRow(nil), view.rejected...),
	}
	for _, s := range view.catalog {
		defs := toolDefs[s.ID]
		provider := GenerationProvider{
			ID:      s.ID,
			Enabled: s.Enabled,
			Status: providerStatus(s, statusFacts{
				lastError:  syncErrors[s.ID],
				synced:     syncOK[s.ID],
				authNeeded: authRequired[s.ID],
				signedIn:   r.Credentials.oauthStore.SignedIn(s.ID),
				toolCount:  len(defs),
			}),
			Tools: make([]GenerationTool, 0, len(defs)),
		}
		for _, def := range defs {
			fingerprint, err := toolDefinitionFingerprint(def)
			if err != nil {
				slog.WarnContext(ctx, "mcp generation fingerprint failed", "provider_id", s.ID, "tool", def.Name, "error", err)
				provider.Status = api.McpStatusError
				provider.Tools = nil
				break
			}
			provider.Tools = append(provider.Tools, GenerationTool{
				Name:        def.Name,
				Title:       def.Title,
				Description: def.Description,
				Schema:      jsonvalue.CloneMap(def.Schema),
				ReadOnly:    def.ReadOnly,
				Fingerprint: fingerprint,
			})
		}
		sort.Slice(provider.Tools, func(i, j int) bool { return provider.Tools[i].Name < provider.Tools[j].Name })
		gen.Providers = append(gen.Providers, provider)
	}
	sort.Slice(gen.Providers, func(i, j int) bool { return gen.Providers[i].ID < gen.Providers[j].ID })
	sort.Slice(gen.Rejected, func(i, j int) bool {
		a, b := gen.Rejected[i], gen.Rejected[j]
		if a.Layer != b.Layer {
			return a.Layer < b.Layer
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Reason < b.Reason
	})
	return finishGeneration(gen)
}

// generationRevisionDomain versions generation interpretation and encoding.
const generationRevisionDomain = "painted-wolf/mcp-resource-generation/2"

func finishGeneration(g *ResourceGeneration) *ResourceGeneration {
	h := sha256.New()
	field := func(label, body string) {
		_, _ = fmt.Fprintf(h, "%s\x1f%d\x1f%s", label, len(body), body)
	}
	field("domain", generationRevisionDomain)
	for _, s := range g.Providers {
		field("provider", s.ID)
		field("enabled", fmt.Sprintf("%t", s.Enabled))
		field("status", string(s.Status))
		for _, t := range s.Tools {
			field("tool", t.Name)
			// The fingerprint covers every model-visible definition field.
			field("fingerprint", t.Fingerprint)
		}
	}
	for _, rej := range g.Rejected {
		field("rejected.layer", string(rej.Layer))
		field("rejected.id", rej.ID)
		field("rejected.reason", rej.Reason)
	}
	g.Revision = hex.EncodeToString(h.Sum(nil))
	return g
}
