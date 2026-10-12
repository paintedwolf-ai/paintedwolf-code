package modelcall

import (
	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

// ModelThinking is the resolved thinking treatment for one (provider, model).
type ModelThinking struct {
	Style        modelinfo.ThinkStyle
	AlwaysOn     bool
	DefaultOn    bool
	EffortLevels modelinfo.ReasoningEffortLevels
}

// ResolveModelThinking intersects model evidence with transport controls.
func ResolveModelThinking(profile providerprofile.Profile, entry modelinfo.Entry, model string) ModelThinking {
	declared, _ := thinkingDeclaration(entry, model)
	if declared != nil && declared.State != "supported" {
		return ModelThinking{Style: modelinfo.ThinkStyleNone}
	}
	rule, ruleOK := modelinfo.MatchThinkingRule(model)
	out := ModelThinking{
		Style:        profile.Thinking,
		AlwaysOn:     entry.ThinkingAlwaysOn || (ruleOK && rule.AlwaysOn),
		DefaultOn:    ruleOK && rule.DefaultOn,
		EffortLevels: rule.EffortLevels,
	}
	if style, ok := modelinfo.ParseThinkStyle(entry.ThinkStyle); ok {
		out.Style = style
	} else if style, ok := modelinfo.ParseThinkStyle(entry.DiscoveredThinkStyle); ok {
		out.Style = style
	} else if ruleOK {
		if style, ok := modelinfo.ParseThinkStyle(rule.Style); ok {
			out.Style = style
		}
	}
	if !profile.SupportsThinkStyle(out.Style) {
		out.Style = profile.Thinking
	}
	if entry.ReasoningEffortLevels != (modelinfo.ReasoningEffortLevels{}) {
		out.EffortLevels = entry.ReasoningEffortLevels
	}
	return out
}

func (thinking ModelThinking) EffortString(level ThinkLevel) (string, bool) {
	if thinking.EffortLevels == (modelinfo.ReasoningEffortLevels{}) {
		return level.EffortString()
	}
	switch level {
	case ThinkOff, ThinkLow:
		return thinking.EffortLevels.Low, true
	case ThinkMedium:
		return thinking.EffortLevels.Medium, true
	case ThinkHigh:
		return thinking.EffortLevels.High, true
	default:
		return "", false
	}
}

// ResolveThinkingCapabilities intersects declared controls with the transport.
func ResolveThinkingCapabilities(profile providerprofile.Profile, entry modelinfo.Entry, model string) modelinfo.ThinkingCapabilities {
	declared, source := thinkingDeclaration(entry, model)
	if declared != nil && declared.State != "supported" {
		out := *declared.Clone()
		out.Source = source
		return out
	}
	mt := ResolveModelThinking(profile, entry, model)
	if mt.Style == modelinfo.ThinkStyleNone {
		return modelinfo.ThinkingCapabilities{State: "unsupported", Source: "transport"}
	}
	if declared == nil {
		return modelinfo.ThinkingCapabilities{State: "unknown"}
	}
	out := *declared.Clone()
	out.Source = source
	if mt.AlwaysOn {
		out.CanDisable = false
	}
	switch mt.Style {
	case modelinfo.ThinkStyleBooleanThink, modelinfo.ThinkStyleThinkingType, modelinfo.ThinkStyleReasoningToggle:
		out.Efforts, out.DefaultEffort, out.Budget = nil, "", nil
	case modelinfo.ThinkStyleEffortLevels, modelinfo.ThinkStyleAdaptive:
		out.Budget = nil
		if mt.Style == modelinfo.ThinkStyleEffortLevels {
			out.CanEnable = false
		}
		if mt.Style == modelinfo.ThinkStyleEffortLevels && profile.ReasoningEffortOff == "" && profile.Thinking != modelinfo.ThinkStyleBooleanThink {
			out.CanDisable = false
		}
	case modelinfo.ThinkStyleBudgetTokens:
		out.Efforts, out.DefaultEffort = nil, ""
		out.CanEnable = false
	case modelinfo.ThinkStyleNone, modelinfo.ThinkStyleReasoningObject:
	}
	if out.Budget != nil && entry.MaxTokens > 0 {
		if out.Budget.Max == 0 || out.Budget.Max >= entry.MaxTokens {
			out.Budget.Max = entry.MaxTokens - 1
		}
		if out.Budget.Max < out.Budget.Min {
			out.Budget = nil
		}
	}
	return out
}

// Explicit declarations outrank capability evidence and family defaults.
func thinkingDeclaration(entry modelinfo.Entry, model string) (*modelinfo.ThinkingCapabilities, string) {
	if entry.Thinking != nil {
		return entry.Thinking, "provider-config"
	}
	if entry.DiscoveredThinking != nil {
		return entry.DiscoveredThinking, "provider-discovery"
	}
	if modelinfo.Refused(entry.EffectiveCapabilities().Reasoning) {
		return &modelinfo.ThinkingCapabilities{State: "unsupported"}, "model-capabilities"
	}
	if rule, ok := modelinfo.MatchThinkingRule(model); ok && rule.Controls != nil {
		return rule.Controls, "model-rule"
	}
	return nil, ""
}
