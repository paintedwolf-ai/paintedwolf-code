package anthropic

import (
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

func (p *Provider) resolveRequestControls(req modelcall.CompletionRequest, model string) (out Request) {
	defer func() {
		if req.AttemptBudget == nil {
			return
		}
		reducedReq := req
		reducedReq.AttemptBudget = nil
		reducedReq.StrictBudget = true
		reduced := p.resolveRequestControls(reducedReq, model)
		req.RecordBudget(out.MaxTokens, []any{out.Thinking, out.OutputConfig}, []any{reduced.Thinking, reduced.OutputConfig})
	}()

	entry, _ := p.ModelEntry(model)
	var temperature *float64
	var override string
	temperature = entry.Temperature
	override = entry.ReasoningEffort

	mt := modelcall.ResolveModelThinking(p.profile, entry, model)
	strict := req.StrictOutputBudget()
	level := modelcall.ResolveThinkLevel(req, entry.ReasoningEffort, strict)
	maxTokens := modelcall.CompletionTokenLimit(req, entry.MaxTokens, providerprofile.DefaultAnthropicMaxTokens, level, mt.Style != modelinfo.ThinkStyleNone)

	out = Request{
		Model:       model,
		MaxTokens:   maxTokens,
		Temperature: temperature,
	}

	if req.ThinkingOverride != nil {
		if req.ThinkingOverrideStyle != "" {
			mt.Style = req.ThinkingOverrideStyle
		}
		applyFixedAnthropicThinking(&out, mt, *req.ThinkingOverride)
		out.MaxTokens = modelcall.CapOutputTokens(out.MaxTokens, req.MaxTokens, entry.MaxTokens)
		return out
	}
	switch mt.Style {
	case modelinfo.ThinkStyleBudgetTokens:
		if budget, on := modelcall.ResolveThinkLevel(req, override, strict).AnthropicBudget(); on {
			budget, on = modelcall.BoundedThinkingBudget(budget, modelcall.CompletionRequest{MaxTokens: maxTokens})
			if on {
				// Thinking needs output headroom and no custom temperature.
				out.Thinking = &anthropicThinking{Type: "enabled", BudgetTokens: budget}
				out.Temperature = nil
			}
		}
	case modelinfo.ThinkStyleAdaptive:
		level := modelcall.ResolveThinkLevel(req, override, strict)
		if level == modelcall.ThinkOff && mt.AlwaysOn && strict {
			level = modelcall.ThinkLow
		}
		if level == modelcall.ThinkOff && mt.DefaultOn && !mt.AlwaysOn {
			out.Thinking = &anthropicThinking{Type: "disabled"}
		} else if effort, on := mt.EffortString(level); on && level != modelcall.ThinkOff {
			out.Thinking = &anthropicThinking{Type: "adaptive", Display: "omitted"}
			out.OutputConfig = &anthropicOutputConfig{Effort: effort}
			out.Temperature = nil
		}
	case modelinfo.ThinkStyleNone, modelinfo.ThinkStyleEffortLevels, modelinfo.ThinkStyleThinkingType,
		modelinfo.ThinkStyleBooleanThink, modelinfo.ThinkStyleReasoningObject, modelinfo.ThinkStyleReasoningToggle:
	}
	return out
}

func applyFixedAnthropicThinking(out *Request, mt modelcall.ModelThinking, override modelcall.ThinkingOverride) {
	if override.Enabled != nil && !*override.Enabled {
		out.Thinking = &anthropicThinking{Type: "disabled"}
		out.OutputConfig = nil
		return
	}
	out.Temperature = nil
	if mt.Style == modelinfo.ThinkStyleAdaptive {
		out.Thinking = &anthropicThinking{Type: "adaptive", Display: "omitted"}
		if override.Effort != "" {
			out.OutputConfig = &anthropicOutputConfig{Effort: override.Effort}
		}
		return
	}
	if override.BudgetTokens != nil {
		budget := *override.BudgetTokens
		if out.MaxTokens <= budget {
			out.MaxTokens = budget + providerprofile.DefaultAnthropicMaxTokens
		}
		out.Thinking = &anthropicThinking{Type: "enabled", BudgetTokens: budget}
	}
}
