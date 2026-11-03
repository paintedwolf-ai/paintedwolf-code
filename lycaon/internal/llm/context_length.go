package llm

import (
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
)

// ContextEntry returns the model entry with its resolved ContextLength and
// ContextLengthSource for provider/model when known.
func ContextEntry(r *Registry, provider, model string) (modelinfo.Entry, bool) {
	if r == nil {
		return modelinfo.Entry{}, false
	}
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if model == "" {
		return modelinfo.Entry{}, false
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return modelinfo.Entry{}, false
	}
	entry, ok := snapshot.entries[provider]
	if !ok {
		return modelinfo.Entry{}, false
	}
	var p modelcall.Provider
	if snapshot.providers != nil {
		p = snapshot.providers[provider]
	}
	for _, m := range entry.Models {
		if modelinfo.EquivalentID(entry.Kind, m.ID, model) && m.ContextLength > 0 {
			out := m
			if out.ContextLengthSource == "" {
				out.ContextLengthSource = "config"
			}
			if oa, ok := providerAdapter(p).(*openaicompat.Provider); ok {
				if runtimeEntry, hasRuntime := oa.ModelEntry(model); hasRuntime && runtimeEntry.ContextLength > 0 && runtimeEntry.ContextLength < out.ContextLength {
					return runtimeEntry, true
				}
			}
			return out, true
		}
	}
	if oa, ok := providerAdapter(p).(*openaicompat.Provider); ok {
		if runtimeEntry, hasRuntime := oa.ModelEntry(model); hasRuntime && runtimeEntry.ContextLength > 0 {
			return runtimeEntry, true
		}
	}
	profile := providerprofile.Default()
	if p != nil {
		profile = p.Profile()
	}
	if r.discovery == nil {
		return modelinfo.Entry{}, false
	}
	cacheKey := discoveryCacheKey(provider, profile, entry.BaseURL, snapshot.credentialValues[provider])
	if cached, ok, _ := r.discovery.Get(cacheKey); ok {
		for _, m := range cached {
			if (m.ID == "*" || modelinfo.EquivalentID(entry.Kind, m.ID, model)) && m.ContextLength > 0 {
				out := m
				out.ID = model
				return out, true
			}
		}
	}
	return modelinfo.Entry{}, false
}

// ContextLength returns the usable context window for provider/model from
// ModelEntry data (catalog YAML, discovery cache, or observed serving window).
// A declared context_length on the catalog entry wins over discovery.
func (r *Registry) ContextLength(provider, model string) int {
	if entry, ok := ContextEntry(r, provider, model); ok && entry.ContextLength > 0 {
		return entry.ContextLength
	}
	return 0
}

func isLocalRegistryProvider(r *Registry, provider string) bool {
	if r == nil {
		return false
	}
	if snapshot := r.snapshot.Load(); snapshot != nil {
		if p := snapshot.providers[strings.TrimSpace(provider)]; p != nil {
			return p.Profile().Local
		}
	}
	return false
}

// providerAdapter reads the actual adapter behind observation middleware.
func providerAdapter(p modelcall.Provider) modelcall.Provider {
	for {
		if logger, ok := p.(*loggingProvider); ok {
			p = logger.inner
			continue
		}
		return p
	}
}
