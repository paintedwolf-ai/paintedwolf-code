package modelcall

import (
	"strings"

	"github.com/lycaon/lycaon/internal/llm/modelinfo"
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
