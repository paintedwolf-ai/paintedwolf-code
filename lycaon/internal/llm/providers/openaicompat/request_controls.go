package openaicompat

import (
	"github.com/lycaon/lycaon/internal/llm/modelcall"
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

type controlOpts struct {
	strictRetry bool
	// reasoning selects the current fallback rung.
	reasoning reasoningFallback
}

type thinkingTypeWire struct {
	Type string `json:"type"`
}

// modelThinking resolves catalog, family, and transport settings.
func (p *Provider) modelThinking(model string) modelcall.ModelThinking {
	entry, _ := p.ModelEntry(model)
	return modelcall.ResolveModelThinking(p.profile, entry, model)
}

// requestControls holds one exclusive reasoning style.
type requestControls struct {
	Temperature     *float64
	MaxTokens       int
	ReasoningEffort string
	Thinking        *thinkingTypeWire
	Reasoning       *modelcall.ReasoningWire
}

func (p *Provider) resolveRequestControls(req modelcall.CompletionRequest, model string, opts controlOpts) (out requestControls) {
	defer func() { p.finishRequestBudget(req, model, opts, &out) }()
	var override string
	if entry, found := p.ModelEntry(model); found {
		out.Temperature = entry.Temperature
		override = entry.ReasoningEffort
	}

	strict := opts.strictRetry || req.StrictOutputBudget()

	mt := p.modelThinking(model)
	if req.ThinkingOverride != nil {
		if req.ThinkingOverrideStyle != "" {
			mt.Style = req.ThinkingOverrideStyle
		}
		applyFixedOpenAIThinking(&out, mt, p.profile, *req.ThinkingOverride)
		return out
	}
	fallback := p.reasoningFallbackFor(model, opts)
	if mt.Style == modelinfo.ThinkStyleNone || fallback == reasoningFallbackOmit {
		return out
	}
	if fallback == reasoningFallbackOff {
		// Only effort levels declare an explicit off token.
		if mt.AlwaysOn || mt.Style != modelinfo.ThinkStyleEffortLevels || p.profile.ReasoningEffortOff == "" {
			return out
		}
		out.ReasoningEffort = p.profile.ReasoningEffortOff
		return out
	}

	lvl := modelcall.ResolveThinkLevel(req, override, strict)

	switch mt.Style {
	case modelinfo.ThinkStyleThinkingType:
		if typ, send := lvl.ThinkingTypeValue(); send {
			if typ == "disabled" && mt.AlwaysOn {
				return out
			}
			out.Thinking = &thinkingTypeWire{Type: typ}
		}
	case modelinfo.ThinkStyleEffortLevels:
		// Structured utility calls omit disabled effort.
		if lvl == modelcall.ThinkOff && req.ResponseFormat != nil && !mt.AlwaysOn {
			break
		}
		if effort, send := mt.EffortString(lvl); send {
			out.ReasoningEffort = effort
		}
	case modelinfo.ThinkStyleReasoningObject, modelinfo.ThinkStyleReasoningToggle:
		if mt.Style == modelinfo.ThinkStyleReasoningObject && mt.AlwaysOn && lvl == modelcall.ThinkOff && mt.EffortLevels != (modelinfo.ReasoningEffortLevels{}) {
			lvl = modelcall.ThinkLow
		}
		if reasoning, send := lvl.ReasoningObject(mt.Style); send {
			if reasoning.Disables() && mt.AlwaysOn {
				return out
			}
			out.Reasoning = reasoning
			if reasoning.Effort != "" {
				out.Reasoning.Effort, _ = mt.EffortString(lvl)
			}
		}
	case modelinfo.ThinkStyleNone, modelinfo.ThinkStyleBooleanThink, modelinfo.ThinkStyleBudgetTokens,
		modelinfo.ThinkStyleAdaptive:
	}
	if p.reasoningActive(mt, out) {
		out.Temperature = nil
	}
	return out
}

// reasoningFallbackFor selects the strongest live or cached rung.
func (p *Provider) reasoningFallbackFor(model string, opts controlOpts) reasoningFallback {
	if cached := cachedReasoningFallback(p.id, model); cached > opts.reasoning {
		return cached
	}
	return opts.reasoning
}

// requestHasReasoningControl reports whether this attempt sent a control.
func (p *Provider) requestHasReasoningControl(req modelcall.CompletionRequest, model string, opts controlOpts) bool {
	controls := p.resolveRequestControls(req, model, opts)
	return controls.ReasoningEffort != "" || controls.Thinking != nil || controls.Reasoning != nil
}

// reasoningActive reports explicit reasoning states that exclude temperature.
func (p *Provider) reasoningActive(mt modelcall.ModelThinking, controls requestControls) bool {
	switch mt.Style {
	case modelinfo.ThinkStyleThinkingType:
		if mt.AlwaysOn {
			return true
		}
		return controls.Thinking != nil && controls.Thinking.Type == "enabled"
	case modelinfo.ThinkStyleReasoningObject, modelinfo.ThinkStyleReasoningToggle:
		if mt.AlwaysOn {
			return true
		}
		return controls.Reasoning != nil && !controls.Reasoning.Disables()
	default:
		return false
	}
}

func (p *Provider) finishRequestBudget(req modelcall.CompletionRequest, model string, opts controlOpts, out *requestControls) {
	entry, _ := p.ModelEntry(model)
	level := modelcall.ResolveThinkLevel(req, entry.ReasoningEffort, opts.strictRetry || req.StrictOutputBudget())
	reasoning := out.ReasoningEffort != "" || out.Thinking != nil && out.Thinking.Type == "enabled" || out.Reasoning != nil && !out.Reasoning.Disables()
	budgetReq := req
	budgetReq.StrictBudget = budgetReq.StrictBudget || opts.strictRetry
	// Native fixed efforts are not ordered by the host ladder. Reserve the
	// largest application allowance without reinterpreting the fixed setting.
	if req.ThinkingOverride != nil && reasoning {
		level = modelcall.ThinkHigh
	}
	out.MaxTokens = modelcall.CompletionTokenLimit(budgetReq, entry.MaxTokens, 0, level, reasoning)
	if req.AttemptBudget == nil {
		return
	}
	reducedReq := req
	reducedReq.AttemptBudget = nil
	reducedReq.StrictBudget = true
	reduced := p.resolveRequestControls(reducedReq, model, controlOpts{strictRetry: true, reasoning: opts.reasoning})
	current := *out
	current.MaxTokens, reduced.MaxTokens = 0, 0
	current.Temperature, reduced.Temperature = nil, nil
	req.RecordBudget(out.MaxTokens, current, reduced)
	mt := p.modelThinking(model)
	if mt.AlwaysOn && (mt.Style == modelinfo.ThinkStyleThinkingType || mt.Style == modelinfo.ThinkStyleReasoningToggle || mt.Style == modelinfo.ThinkStyleReasoningObject && reduced.Reasoning == nil) {
		req.AttemptBudget.CanReduceReasoning = false
	}
}

func applyFixedOpenAIThinking(out *requestControls, mt modelcall.ModelThinking, profile providerprofile.Profile, override modelcall.ThinkingOverride) {
	enabled := override.Enabled == nil || *override.Enabled
	switch mt.Style {
	case modelinfo.ThinkStyleEffortLevels:
		if enabled {
			out.ReasoningEffort = override.Effort
		} else {
			out.ReasoningEffort = profile.ReasoningEffortOff
		}
	case modelinfo.ThinkStyleThinkingType:
		typ := "disabled"
		if enabled {
			typ = "enabled"
		}
		out.Thinking = &thinkingTypeWire{Type: typ}
	case modelinfo.ThinkStyleReasoningObject, modelinfo.ThinkStyleReasoningToggle:
		out.Reasoning = &modelcall.ReasoningWire{Effort: override.Effort, Enabled: override.Enabled, MaxTokens: override.BudgetTokens}
	case modelinfo.ThinkStyleNone, modelinfo.ThinkStyleBooleanThink, modelinfo.ThinkStyleBudgetTokens,
		modelinfo.ThinkStyleAdaptive:
	}
	if enabled {
		out.Temperature = nil
	}
}
