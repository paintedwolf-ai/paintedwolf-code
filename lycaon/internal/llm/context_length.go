package llm

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/transcript"
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
			if prepared, err := policy.prepare(ctx, req); err == nil {
				req = prepared
			}
		}
		if reporter, ok := provider.(modelcall.RequestBudgetReporter); ok {
			budget = reporter.RequestBudget(ctx, req)
		}
	}
	budget.MinimumOutput = max(budget.MinimumOutput, modelcall.MinimumCompletionOutput(req))
	if limit := r.ContextLength(providerID, req.Model); limit > 0 && (budget.Window <= 0 || limit < budget.Window) {
		budget.Window = limit
	}
	return budget
}
