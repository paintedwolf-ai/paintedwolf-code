package modelcall

import (
	"context"
	"encoding/json"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/tokenest"
)

// RequestBudget describes the complete input and transport reservation.
type RequestBudget struct {
	Window   int
	Input    int
	Output   int
	Headroom int
}

type RequestBudgetReporter interface {
	RequestBudget(context.Context, CompletionRequest) RequestBudget
}

// RequestBudget supplies a protocol-neutral estimate for catalog-backed adapters.
func (c Catalog) RequestBudget(_ context.Context, req CompletionRequest) RequestBudget {
	entry, _ := c.ModelEntry(c.ResolveModel(req))
	return RequestBudget{
		Window: entry.ContextLength, Input: EstimateRequestInput(req), Headroom: 1024,
		Output: CompletionTokenLimit(req, entry.MaxTokens, OrchestrationMaxTokens, req.Think,
			modelinfo.Supported(entry.EffectiveCapabilities().Reasoning)),
	}
}

func EstimateRequestInput(req CompletionRequest) int {
	tokens := 0
	for _, row := range req.Messages {
		tokens += tokenest.EstimateDefault(row.Content) + 16
		if row.ModelReasoning != nil {
			tokens += tokenest.EstimateDefault(row.ModelReasoning.Text)
			for _, detail := range row.ModelReasoning.Details {
				tokens += tokenest.EstimateDefault(string(detail))
			}
		}
		if raw, err := json.Marshal(row.ToolCalls); err == nil {
			tokens += tokenest.EstimateDefault(string(raw))
		}
	}
	for _, tool := range req.Tools {
		tokens += tokenest.EstimateDefault(tool.Name) + tokenest.EstimateDefault(tool.Description) + 16
		if raw, err := json.Marshal(tool.ArgsSchema); err == nil {
			tokens += tokenest.EstimateDefault(string(raw))
		}
	}
	return tokens
}
