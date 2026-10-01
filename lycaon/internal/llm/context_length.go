package llm

import (
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

// ContextLength returns the usable context window for provider/model from
// already-known ModelEntry data only (catalog YAML + discovery cache). It does
// not perform live HTTP discovery.
func (r *Registry) ContextLength(provider, model string) int {
	if r == nil {
		return 0
	}
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if model == "" {
		return 0
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return 0
	}
	entry, ok := snapshot.entries[provider]
	if !ok {
		return 0
	}
	for _, m := range entry.Models {
		if modelinfo.EquivalentID(entry.Kind, m.ID, model) && m.ContextLength > 0 {
			return m.ContextLength
		}
	}
	profile := providerprofile.Default()
	if p := snapshot.providers[provider]; p != nil {
		profile = p.Profile()
	}
	cacheKey := discoveryCacheKey(provider, profile, entry.BaseURL, snapshot.credentialValues[provider])
	if cached, ok, _ := r.discovery.Get(cacheKey); ok {
		for _, m := range cached {
			if modelinfo.EquivalentID(entry.Kind, m.ID, model) && m.ContextLength > 0 {
				return m.ContextLength
			}
		}
	}
	return 0
}
