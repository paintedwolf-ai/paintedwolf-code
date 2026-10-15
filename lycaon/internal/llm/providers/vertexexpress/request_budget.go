package vertexexpress

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
)

func (p *Provider) RequestBudget(ctx context.Context, req modelcall.CompletionRequest) modelcall.RequestBudget {
	req.AttemptBudget = nil
	budget := providerwire.CatalogRequestBudget(ctx, req, p.Catalog)
	budget.Output = p.resolveRequestControls(req, p.ResolveModel(req)).MaxOutputTokens
	return budget
}
