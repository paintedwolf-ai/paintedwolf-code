package bedrock

import (
	"context"

	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
	"github.com/lycaon/lycaon/internal/llm/providerwire"
)

func (p *Provider) RequestBudget(ctx context.Context, req modelcall.CompletionRequest) modelcall.RequestBudget {
	budget := providerwire.CatalogRequestBudget(ctx, req, p.Catalog)
	entry, _ := p.ModelEntry(p.ResolveModel(req))
	budget.Output = modelcall.CompletionTokenLimit(req, entry.MaxTokens, providerprofile.DefaultAnthropicMaxTokens, modelcall.ThinkOff, false)
	return budget
}
