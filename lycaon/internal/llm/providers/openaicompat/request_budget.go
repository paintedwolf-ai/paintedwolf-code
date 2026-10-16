package openaicompat

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
)

func (p *Provider) RequestBudget(ctx context.Context, req modelcall.CompletionRequest) modelcall.RequestBudget {
	req.AttemptBudget = nil
	budget := providerwire.CatalogRequestBudget(ctx, req, p.Catalog)
	if output := p.resolveRequestControls(req, p.ResolveModel(req), controlOpts{}).MaxTokens; output > 0 {
		budget.Output = output
	}
	return budget
}
