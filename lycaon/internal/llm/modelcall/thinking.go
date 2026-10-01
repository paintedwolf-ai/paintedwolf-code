package modelcall

import (
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
	"github.com/lycaon/lycaon/internal/llm/providerprofile"
)

// ThinkLevel sets reasoning effort independently from wire format.
type ThinkLevel int

const (
	ThinkUnset ThinkLevel = iota
	ThinkOff
	ThinkLow
	ThinkMedium
	ThinkHigh
)

// ResolveThinkLevel selects effort from budget, request, model, and turn policy.
func ResolveThinkLevel(req CompletionRequest, override string, strict bool) ThinkLevel {
	if strict {
		return ThinkOff
	}
	if req.Think != ThinkUnset {
		return req.Think
	}
	if lvl, ok := ParseThinkLevel(override); ok {
		return lvl
	}
	if req.Composition == CompositionHostUtility {
		return ThinkOff
	}
	return ThinkMedium
}

func ParseThinkLevel(s string) (ThinkLevel, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off", "none", "false":
		return ThinkOff, true
	case "low":
		return ThinkLow, true
	case "medium":
		return ThinkMedium, true
	case "high":
		return ThinkHigh, true
	default:
		return ThinkUnset, false
	}
}

// EffortString maps a level to the compatible reasoning field.
func (lvl ThinkLevel) EffortString() (string, bool) {
	switch lvl {
	case ThinkOff, ThinkLow:
		return MinimumReasoningEffort, true
	case ThinkMedium:
		return "medium", true
	case ThinkHigh:
		return "high", true
	default:
		return "", false
	}
}

// OllamaThink maps effort to the transport's boolean-or-level option.
func (lvl ThinkLevel) OllamaThink() any {
	switch lvl {
	case ThinkOff:
		return false
	case ThinkLow:
		return "low"
	case ThinkMedium:
		return "medium"
	case ThinkHigh:
		return "high"
	default:
		return nil
	}
}

// AnthropicBudget gives enabled effort levels a non-zero thinking budget.
func (lvl ThinkLevel) AnthropicBudget() (int, bool) {
	switch lvl {
	case ThinkLow:
		return 1024, true
	case ThinkMedium:
		return 8192, true
	case ThinkHigh:
		return 16384, true
	default:
		return 0, false
	}
}

// GeminiThinkingBudget omits zero because some model families reject it.
func (lvl ThinkLevel) GeminiThinkingBudget() (int, bool) {
	switch lvl {
	case ThinkLow:
		return 1024, true
	case ThinkMedium:
		return 8192, true
	case ThinkHigh:
		return 16384, true
	default:
		return 0, false
	}
}

// ThinkingTypeValue maps effort to the enabled state.
func (lvl ThinkLevel) ThinkingTypeValue() (typ string, send bool) {
	switch lvl {
	case ThinkOff:
		return "disabled", true
	case ThinkLow:
		return "enabled", true
	case ThinkMedium, ThinkHigh:
		return "enabled", true
	default:
		return "", false
	}
}

// ReasoningObject handles both boolean and effort-based controls.
func (lvl ThinkLevel) ReasoningObject(style modelinfo.ThinkStyle) (*ReasoningWire, bool) {
	if style == modelinfo.ThinkStyleReasoningToggle {
		switch lvl {
		case ThinkOff, ThinkLow, ThinkMedium, ThinkHigh:
			enabled := lvl != ThinkOff
			return &ReasoningWire{Enabled: &enabled}, true
		default:
			return nil, false
		}
	}
	switch lvl {
	case ThinkOff:
		off := false
		return &ReasoningWire{Enabled: &off}, true
	case ThinkLow:
		return &ReasoningWire{Effort: "low"}, true
	case ThinkMedium:
		return &ReasoningWire{Effort: "medium"}, true
	case ThinkHigh:
		return &ReasoningWire{Effort: "high"}, true
	default:
		return nil, false
	}
}

// BoundedThinkingBudget keeps hidden reasoning within explicit output limits.
func BoundedThinkingBudget(budget int, req CompletionRequest) (int, bool) {
	if req.MaxTokens > 0 && budget >= req.MaxTokens {
		budget = req.MaxTokens / 2
	}
	return budget, budget >= 1024
}

// ReasoningWire preserves an explicit false value.
type ReasoningWire struct {
	MaxTokens *int   `json:"max_tokens,omitempty"`
	Effort    string `json:"effort,omitempty"`
	Enabled   *bool  `json:"enabled,omitempty"`
}

// Disables reports an explicit reasoning opt-out.
func (r *ReasoningWire) Disables() bool {
	return r != nil && r.Enabled != nil && !*r.Enabled
}

// ThinkingOverride is a human-owned policy for every call to one provider/model.
// Fixed settings carry native values; application explicitly clears inheritance.
type ThinkingOverride struct {
	ProviderID   string `yaml:"provider_id" json:"provider_id"`
	Model        string `yaml:"model" json:"model"`
	Mode         string `yaml:"mode" json:"mode"`
	Effort       string `yaml:"effort,omitempty" json:"effort,omitempty"`
	Enabled      *bool  `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	BudgetTokens *int   `yaml:"budget_tokens,omitempty" json:"budget_tokens,omitempty"`
}

// The reasoning wire form follows the model family, not the hosting provider.
// Family rules live in providers.yaml under `model_thinking:`; the transport
// profile limits what can be carried.

// ModelThinking is the resolved thinking treatment for one (provider, model).
type ModelThinking struct {
	Style        modelinfo.ThinkStyle
	AlwaysOn     bool
	DefaultOn    bool
	EffortLevels modelinfo.ReasoningEffortLevels
}

// ResolveModelThinking is the single resolution point for a model's thinking
// treatment: per-model catalog override → family rule → transport default,
// constrained to the wire forms the driver can emit. AlwaysOn accumulates from
// both the family rule and the per-model entry.
func ResolveModelThinking(profile providerprofile.Profile, entry modelinfo.Entry, model string) ModelThinking {
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
	mt := ResolveModelThinking(profile, entry, model)
	if mt.Style == modelinfo.ThinkStyleNone {
		return modelinfo.ThinkingCapabilities{State: "unsupported", Source: "transport"}
	}
	var declared *modelinfo.ThinkingCapabilities
	source := ""
	if entry.Thinking != nil {
		declared, source = entry.Thinking, "provider-config"
	} else if entry.DiscoveredThinking != nil {
		declared, source = entry.DiscoveredThinking, "provider-discovery"
	} else if rule, ok := modelinfo.MatchThinkingRule(model); ok && rule.Controls != nil {
		declared, source = rule.Controls, "model-rule"
	}
	if declared == nil {
		return modelinfo.ThinkingCapabilities{State: "unknown"}
	}
	out := *declared.Clone()
	out.Source = source
	if out.State != "supported" {
		return out
	}
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
