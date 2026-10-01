package vertexexpress

import (
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
)

func (p *Provider) resolveRequestControls(req modelcall.CompletionRequest, model string) (cfg vertexExpressGenerationConfig) {
	defer func() {
		if req.AttemptBudget == nil {
			return
		}
		reducedReq := req
		reducedReq.AttemptBudget = nil
		reducedReq.StrictBudget = true
		reduced := p.resolveRequestControls(reducedReq, model)
		req.RecordBudget(cfg.MaxOutputTokens, cfg.ThinkingConfig, reduced.ThinkingConfig)
	}()

	entry, _ := p.ModelEntry(model)
	mt := modelcall.ResolveModelThinking(p.profile, entry, model)
	strict := req.StrictOutputBudget()
	level := modelcall.ResolveThinkLevel(req, entry.ReasoningEffort, strict)
	maxTokens := modelcall.CompletionTokenLimit(req, entry.MaxTokens, vertexExpressDefaultMaxTokens, level, mt.Style != modelinfo.ThinkStyleNone)

	cfg = vertexExpressGenerationConfig{
		Temperature:     entry.Temperature,
		MaxOutputTokens: maxTokens,
	}
	// The endpoint accepts only a schema subset, so project the JSON MIME type.
	if req.ResponseFormat != nil {
		switch req.ResponseFormat.Type {
		case modelcall.ResponseFormatJSONObject, modelcall.ResponseFormatJSONSchema:
			cfg.ResponseMIMEs = "application/json"
		}
	}

	if mt.Style == modelinfo.ThinkStyleEffortLevels {
		if effort, ok := mt.EffortString(modelcall.ResolveThinkLevel(req, entry.ReasoningEffort, strict)); ok {
			cfg.ThinkingConfig = &vertexExpressThinkingConfig{ThinkingLevel: effort, IncludeThoughts: true}
		}
	}
	if mt.Style == modelinfo.ThinkStyleBudgetTokens {
		if level == modelcall.ThinkOff && strict {
			capabilities := modelcall.ResolveThinkingCapabilities(p.profile, entry, model)
			budget := 1024
			if capabilities.Budget != nil {
				budget = capabilities.Budget.Min
			}
			if capabilities.CanDisable {
				budget = 0
			}
			if budget < maxTokens {
				cfg.ThinkingConfig = &vertexExpressThinkingConfig{ThinkingBudget: &budget, IncludeThoughts: true}
			}
		}
		if budget, on := modelcall.ResolveThinkLevel(req, entry.ReasoningEffort, strict).GeminiThinkingBudget(); on {
			budget, on = modelcall.BoundedThinkingBudget(budget, modelcall.CompletionRequest{MaxTokens: maxTokens})
			if on {
				// Thinking tokens share the output allowance.
				cfg.ThinkingConfig = &vertexExpressThinkingConfig{
					ThinkingBudget:  &budget,
					IncludeThoughts: true,
				}
			}
		}
	}

	if req.ThinkingOverride != nil {
		applyFixedVertexThinking(&cfg, *req.ThinkingOverride)
		cfg.MaxOutputTokens = modelcall.CapOutputTokens(cfg.MaxOutputTokens, req.MaxTokens, entry.MaxTokens)
	}
	return cfg
}
func applyFixedVertexThinking(out *vertexExpressGenerationConfig, override modelcall.ThinkingOverride) {
	if override.Effort != "" {
		out.ThinkingConfig = &vertexExpressThinkingConfig{ThinkingLevel: override.Effort, IncludeThoughts: true}
		return
	}
	if override.Enabled != nil && !*override.Enabled {
		budget := 0
		out.ThinkingConfig = &vertexExpressThinkingConfig{ThinkingBudget: &budget}
		return
	}
	if override.BudgetTokens != nil {
		budget := *override.BudgetTokens
		if out.MaxOutputTokens <= budget {
			out.MaxOutputTokens = budget + vertexExpressDefaultMaxTokens
		}
		out.ThinkingConfig = &vertexExpressThinkingConfig{ThinkingBudget: &budget, IncludeThoughts: true}
	}
}
