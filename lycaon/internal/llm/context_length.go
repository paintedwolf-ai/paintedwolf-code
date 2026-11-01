package llm

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providers/openaicompat"
	"github.com/lycaon/lycaon/internal/llm/transcript"
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
			if oa, ok := p.(*openaicompat.Provider); ok {
				if runtimeEntry, hasRuntime := oa.ModelEntry(model); hasRuntime && runtimeEntry.ContextLength > 0 && runtimeEntry.ContextLength < out.ContextLength {
					return runtimeEntry, true
				}
			}
			return out, true
		}
	}
	if oa, ok := p.(*openaicompat.Provider); ok {
		if runtimeEntry, hasRuntime := oa.ModelEntry(model); hasRuntime && runtimeEntry.ContextLength > 0 {
			return runtimeEntry, true
		}
	}
	profile := providerprofile.Default()
	if p != nil {
		profile = p.Profile()
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
	if _, present, _, _ := r.discovery.Snapshot(cacheKey); !present {
		if oa, ok := p.(*openaicompat.Provider); ok && profile.Local && strings.TrimSpace(entry.BaseURL) != "" {
			if limit := oa.ContextLimit(context.Background(), model); limit > 0 {
				if runtimeEntry, hasRuntime := oa.ModelEntry(model); hasRuntime && runtimeEntry.ContextLength > 0 {
					return runtimeEntry, true
				}
				return modelinfo.Entry{ID: model, ContextLength: limit}, true
			}
		}
	}
	return modelinfo.Entry{}, false
}

// ContextLength returns the usable context window for provider/model from
// ModelEntry data (catalog YAML, discovery cache, or local provider serving-window
// probe). A declared context_length on the catalog entry wins over discovery.
func (r *Registry) ContextLength(provider, model string) int {
	if entry, ok := ContextEntry(r, provider, model); ok && entry.ContextLength > 0 {
		return entry.ContextLength
	}
	return 0
}

func isLocalRegistryProvider(r *Registry, provider string) bool {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		return false
	}
	if r != nil {
		if snapshot := r.snapshot.Load(); snapshot != nil {
			if p := snapshot.providers[provider]; p != nil && p.Profile().Local {
				return true
			}
			if entry, ok := snapshot.entries[provider]; ok {
				if entry.LocalFree || IsLocalProviderKind(entry.Kind) {
					return true
				}
			}
		}
	}
	return IsLocalProviderKind(provider)
}

// RecordContextWindow records an observed context window (for example from a
// structured provider context-overflow rejection) for provider/model.
func RecordContextWindow(r *Registry, provider, model string, window int, source string, overflow bool) {
	if r == nil || window <= 0 {
		return
	}
	snapshot := r.snapshot.Load()
	if snapshot == nil {
		return
	}
	if p := snapshot.providers[strings.TrimSpace(provider)]; p != nil {
		if oa, ok := p.(*openaicompat.Provider); ok {
			oa.RecordContextWindow(model, window, source, overflow)
		}
	}
}

func (r *Registry) RequestBudget(ctx context.Context, providerID string, req modelcall.CompletionRequest) modelcall.RequestBudget {
	req.Messages = transcript.Project(req.Messages)
	budget := modelcall.RequestBudget{Input: modelcall.EstimateRequestInput(req), Output: modelcall.CompletionTokenLimit(req, 0, modelcall.OrchestrationMaxTokens, req.Think, false), Headroom: 1024}
	if r == nil {
		return budget
	}
	// Planning reads the adapter without dispatch middleware.
	if snapshot := r.snapshot.Load(); snapshot != nil {
		provider := snapshot.providers[providerID]
		if provider != nil {
			policy := thinkingPolicyProvider{Provider: provider, registry: r}
			if prepared, err := policy.prepare(ctx, req, nil); err == nil {
				req = prepared
			}
		}
		if reporter, ok := provider.(modelcall.RequestBudgetReporter); ok {
			budget = reporter.RequestBudget(ctx, req)
		}
	}
	budget.MinimumOutput = max(budget.MinimumOutput, modelcall.MinimumCompletionOutput(req))
	if limit := r.ContextLength(providerID, req.Model); limit > 0 && (budget.Window <= 0 || limit < budget.Window) { //nolint:contextcheck // synchronous lookup
		budget.Window = limit
	}
	return budget
}
