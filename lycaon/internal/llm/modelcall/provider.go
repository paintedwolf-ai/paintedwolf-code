// Package modelcall defines provider request and response values, per-attempt controls, and completion budget policy.
package modelcall

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/pkg/api"
)

// ModelInfo describes a model exposed by a provider.
type ModelInfo struct {
	ID          string
	Description string
	Vision      bool
}

// Provider is an AI provider adapter.
type Provider interface {
	ID() string
	Complete(ctx context.Context, req CompletionRequest) (*Completion, error)
	Stream(ctx context.Context, req CompletionRequest) (<-chan StreamChunk, error)
	Models() []ModelInfo
	// Profile reports the provider's transport behavior (tool calling,
	// streaming, thinking style, discovery).
	Profile() providerprofile.Profile
}

// ProviderRegistry registers and resolves model providers.
type ProviderRegistry interface {
	Register(p Provider) error
	Get(id string) (Provider, error)
	List(ctx context.Context) []api.ProviderMeta
	Default() Provider
}

// Catalog is a provider's configured model list: the ModelInfo it reports
// and the catalog entries its wire projection reads per model. Providers
// embed it, so Models satisfies Provider.
type Catalog struct {
	// vendor selects modelinfo.EquivalentID aliasing for entry lookups.
	vendor  string
	infos   []ModelInfo
	entries map[string]modelinfo.Entry
}

// NewCatalog indexes models for a provider of the given vendor.
func NewCatalog(vendor string, models []modelinfo.Entry) Catalog {
	c := Catalog{
		vendor:  vendor,
		infos:   make([]ModelInfo, 0, len(models)),
		entries: make(map[string]modelinfo.Entry, len(models)),
	}
	for _, m := range models {
		c.infos = append(c.infos, ModelInfo{ID: m.ID, Vision: modelinfo.Supported(m.EffectiveCapabilities().Vision)})
		c.entries[m.ID] = m
	}
	return c
}

// WithModels replaces the model list, keeping the vendor.
func (c Catalog) WithModels(models []modelinfo.Entry) Catalog {
	return NewCatalog(c.vendor, models)
}

func (c Catalog) Models() []ModelInfo {
	return append([]ModelInfo(nil), c.infos...)
}

// ModelEntry finds a model's catalog entry, exactly or by vendor alias.
func (c Catalog) ModelEntry(model string) (modelinfo.Entry, bool) {
	if entry, ok := c.entries[model]; ok {
		return entry, true
	}
	for id, entry := range c.entries {
		if modelinfo.EquivalentID(c.vendor, id, model) {
			return entry, true
		}
	}
	return modelinfo.Entry{}, false
}

// ResolveModel returns the requested model, defaulting to the first one.
func (c Catalog) ResolveModel(req CompletionRequest) string {
	if req.Model != "" {
		return req.Model
	}
	if len(c.infos) > 0 {
		return c.infos[0].ID
	}
	return ""
}
