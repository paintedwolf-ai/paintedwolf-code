package llm

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
)

func (r *Registry) RequestBudget(ctx context.Context, providerID string, req modelcall.CompletionRequest) modelcall.RequestBudget {
	budget := modelcall.RequestBudget{Input: modelcall.EstimateRequestInput(req), Output: modelcall.CompletionTokenLimit(req, 0, modelcall.OrchestrationMaxTokens, req.Think, false), Headroom: 1024}
	if r == nil {
		return budget
	}
	provider, err := r.Get(providerID)
	if err == nil {
		if reporter, ok := provider.(modelcall.RequestBudgetReporter); ok {
			budget = reporter.RequestBudget(ctx, req)
		}
	}
	if budget.Window <= 0 {
		budget.Window = r.ContextLength(providerID, req.Model)
	}
	return budget
}

func (p *loggingProvider) RequestBudget(ctx context.Context, req modelcall.CompletionRequest) modelcall.RequestBudget {
	if reporter, ok := p.inner.(modelcall.RequestBudgetReporter); ok {
		return reporter.RequestBudget(ctx, req)
	}
	return modelcall.RequestBudget{Input: modelcall.EstimateRequestInput(req), Output: modelcall.CompletionTokenLimit(req, 0, modelcall.OrchestrationMaxTokens, req.Think, false), Headroom: 1024}
}
