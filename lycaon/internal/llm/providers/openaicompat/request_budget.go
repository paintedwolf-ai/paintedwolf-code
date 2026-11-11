package openaicompat

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
)

func (p *Provider) RequestBudget(ctx context.Context, req modelcall.CompletionRequest) modelcall.RequestBudget {
	req.AttemptBudget = nil
	budget := providerwire.CatalogRequestBudget(ctx, req, p.Catalog)
	model := p.ResolveModel(req)
	if budget.Window <= 0 {
		if p.profile.Local {
			budget.Window = p.ContextLimit(ctx, model)
		} else if entry, ok := p.ModelEntry(model); ok && entry.ContextLength > 0 {
			budget.Window = entry.ContextLength
		}
	} else if entry, ok := p.ModelEntry(model); ok && entry.ContextLength > 0 && entry.ContextLength < budget.Window {
		budget.Window = entry.ContextLength
	}
	if output := p.resolveRequestControls(req, model, controlOpts{}).MaxTokens; output > 0 {
		budget.Output = output
	}
	return budget
}
