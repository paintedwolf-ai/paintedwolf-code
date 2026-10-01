package modelinfo

import (
	"fmt"
	"slices"
	"strings"
	"sync/atomic"
)

// ThinkingCapabilities declares native controls independently of host policy.
// A missing declaration is unknown, never a guessed low/medium/high ladder.
type ThinkingCapabilities struct {
	State         string               `yaml:"state" json:"state"`
	Efforts       []string             `yaml:"efforts,omitempty" json:"efforts,omitempty"`
	CanEnable     bool                 `yaml:"can_enable,omitempty" json:"can_enable,omitempty"`
	CanDisable    bool                 `yaml:"can_disable,omitempty" json:"can_disable,omitempty"`
	Budget        *ThinkingBudgetRange `yaml:"budget,omitempty" json:"budget,omitempty"`
	DefaultEffort string               `yaml:"default_effort,omitempty" json:"default_effort,omitempty"`
	Source        string               `yaml:"-" json:"source,omitempty"`
}

type ThinkingBudgetRange struct {
	Min int `yaml:"min" json:"min"`
	Max int `yaml:"max,omitempty" json:"max,omitempty"`
}

func (c *ThinkingCapabilities) Clone() *ThinkingCapabilities {
	if c == nil {
		return nil
	}
	out := *c
	out.Efforts = slices.Clone(c.Efforts)
	if c.Budget != nil {
		budget := *c.Budget
		out.Budget = &budget
	}
	return &out
}

func (c *ThinkingCapabilities) Validate() error {
	if c == nil {
		return nil
	}
	if c.State != "supported" && c.State != "unsupported" && c.State != "unknown" {
		return fmt.Errorf("unknown thinking capability state %q", c.State)
	}
	if c.State != "supported" && (len(c.Efforts) > 0 || c.CanEnable || c.CanDisable || c.Budget != nil || c.DefaultEffort != "") {
		return fmt.Errorf("thinking controls require supported state")
	}
	seen := make(map[string]bool)
	for _, effort := range c.Efforts {
		if !ValidReasoningToken(effort) || seen[effort] || effort == "none" || effort == "off" {
			return fmt.Errorf("invalid or duplicate thinking effort %q; disable uses can_disable", effort)
		}
		seen[effort] = true
	}
	if c.DefaultEffort != "" && !seen[c.DefaultEffort] {
		return fmt.Errorf("default thinking effort must be supported")
	}
	if c.Budget != nil && (c.Budget.Min <= 0 || c.Budget.Max != 0 && c.Budget.Max < c.Budget.Min) {
		return fmt.Errorf("thinking budget requires positive min and optional max >= min")
	}
	return nil
}

// ReasoningEffortLevels translates host intent into a protocol's named levels.
type ReasoningEffortLevels struct {
	Low    string `yaml:"low"`
	Medium string `yaml:"medium"`
	High   string `yaml:"high"`
}

func (levels ReasoningEffortLevels) Validate() error {
	if levels == (ReasoningEffortLevels{}) {
		return nil
	}
	for _, value := range []string{levels.Low, levels.Medium, levels.High} {
		if !ValidReasoningToken(value) {
			return fmt.Errorf("reasoning effort levels require low, medium, and high protocol tokens")
		}
	}
	return nil
}

func ValidReasoningToken(value string) bool {
	return value != "" && len(value) <= 32 && !strings.ContainsFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-')
	})
}

// ThinkStyle identifies a reasoning-control wire form.
type ThinkStyle string

const (
	// ThinkStyleNone: no reasoning control.
	ThinkStyleNone ThinkStyle = "none"
	// ThinkStyleEffortLevels: reasoning_effort (low|medium|high).
	ThinkStyleEffortLevels ThinkStyle = "effort_levels"
	// ThinkStyleThinkingType: thinking.type (enabled|disabled).
	ThinkStyleThinkingType ThinkStyle = "thinking_type"
	// ThinkStyleBooleanThink: boolean `think` toggle; level-aware models graduate it.
	ThinkStyleBooleanThink ThinkStyle = "boolean_think"
	// ThinkStyleBudgetTokens: thinking token budget.
	ThinkStyleBudgetTokens ThinkStyle = "budget_tokens"
	// ThinkStyleAdaptive combines adaptive reasoning with effort.
	ThinkStyleAdaptive ThinkStyle = "adaptive"
	// ThinkStyleReasoningObject uses one structured reasoning control.
	ThinkStyleReasoningObject ThinkStyle = "reasoning_object"
	// ThinkStyleReasoningToggle uses reasoning.enabled without effort levels.
	ThinkStyleReasoningToggle ThinkStyle = "reasoning_toggle"
)

// ThinkingRule maps model-id substrings to a thinking wire style. First
// matching rule wins, so specific rules (a model that can't disable thinking)
// precede their family catch-all.
type ThinkingRule struct {
	Controls *ThinkingCapabilities `yaml:"controls,omitempty"`
	// Match is a list of case-insensitive substrings tested against the model
	// id; any hit selects the rule.
	Match []string `yaml:"match"`
	// Style names a ThinkStyle constant value.
	Style string `yaml:"style"`
	// AlwaysOn marks families that reject disabling reasoning; drivers omit
	// the control instead of sending a disable.
	AlwaysOn bool `yaml:"always_on,omitempty"`
	// DefaultOn marks families where omitting the control enables reasoning.
	// A deliberate ThinkOff must therefore emit the protocol's disabled form.
	DefaultOn bool `yaml:"default_on,omitempty"`
	// EffortLevels maps host intent to the model's named reasoning levels.
	EffortLevels ReasoningEffortLevels `yaml:"effort_levels,omitempty"`
	// ReasoningMarkers declares the tags the family's template writes around
	// reasoning in the content channel, so a host can separate them from
	// visible text whatever transport served the model.
	ReasoningMarkers *ReasoningMarkers `yaml:"reasoning_markers,omitempty"`
}

// ReasoningMarkers are the literal open and close tags a model family writes
// around reasoning when a serving stack leaves it in the content channel.
type ReasoningMarkers struct {
	Open  string `yaml:"open"`
	Close string `yaml:"close"`
}

// maxReasoningMarkerLen bounds a marker to a short literal tag.
const maxReasoningMarkerLen = 32

func (m *ReasoningMarkers) Validate() error {
	if m == nil {
		return nil
	}
	for name, tag := range map[string]string{"open": m.Open, "close": m.Close} {
		if tag == "" || len(tag) > maxReasoningMarkerLen || strings.TrimSpace(tag) != tag {
			return fmt.Errorf("reasoning_markers.%s must be a literal tag of at most %d bytes without surrounding whitespace", name, maxReasoningMarkerLen)
		}
	}
	if m.Open == m.Close {
		return fmt.Errorf("reasoning_markers open and close must differ")
	}
	return nil
}

var modelThinkingRules atomic.Pointer[[]ThinkingRule]

// SetThinkingRules installs the active rule set (user rules first, then
// bundled — first match wins). ProviderCatalog.Reload is the production caller.
func SetThinkingRules(rules []ThinkingRule) {
	own := append([]ThinkingRule(nil), rules...)
	for i := range own {
		own[i].Match = append([]string(nil), own[i].Match...)
		own[i].Controls = own[i].Controls.Clone()
	}
	modelThinkingRules.Store(&own)
}

func ActiveThinkingRules() []ThinkingRule {
	if p := modelThinkingRules.Load(); p != nil {
		return *p
	}
	return nil
}

// ValidateThinkingRules rejects rules whose style is not a known ThinkStyle or
// whose match list is empty — a bad rule set fails the catalog load rather
// than silently misdressing requests.
func ValidateThinkingRules(rules []ThinkingRule) error {
	for i, rule := range rules {
		if err := rule.Controls.Validate(); err != nil {
			return fmt.Errorf("model_thinking[%d]: %w", i, err)
		}
		if len(rule.Match) == 0 {
			return fmt.Errorf("model_thinking[%d]: empty match list", i)
		}
		for _, m := range rule.Match {
			if strings.TrimSpace(m) == "" {
				return fmt.Errorf("model_thinking[%d]: blank match pattern", i)
			}
		}
		style, ok := ParseThinkStyle(rule.Style)
		if !ok {
			return fmt.Errorf("model_thinking[%d]: unknown style %q", i, rule.Style)
		}
		if rule.DefaultOn && style != ThinkStyleAdaptive {
			return fmt.Errorf("model_thinking[%d]: default_on requires adaptive style", i)
		}
		if err := rule.EffortLevels.Validate(); err != nil {
			return fmt.Errorf("model_thinking[%d]: %w", i, err)
		}
		if err := rule.ReasoningMarkers.Validate(); err != nil {
			return fmt.Errorf("model_thinking[%d]: %w", i, err)
		}
	}
	return nil
}

func ParseThinkStyle(s string) (ThinkStyle, bool) {
	switch ThinkStyle(strings.ToLower(strings.TrimSpace(s))) {
	case ThinkStyleNone:
		return ThinkStyleNone, true
	case ThinkStyleEffortLevels:
		return ThinkStyleEffortLevels, true
	case ThinkStyleThinkingType:
		return ThinkStyleThinkingType, true
	case ThinkStyleBooleanThink:
		return ThinkStyleBooleanThink, true
	case ThinkStyleBudgetTokens:
		return ThinkStyleBudgetTokens, true
	case ThinkStyleAdaptive:
		return ThinkStyleAdaptive, true
	case ThinkStyleReasoningObject:
		return ThinkStyleReasoningObject, true
	case ThinkStyleReasoningToggle:
		return ThinkStyleReasoningToggle, true
	default:
		return "", false
	}
}

func MatchThinkingRule(model string) (ThinkingRule, bool) {
	for _, rule := range ActiveThinkingRules() {
		if MatchFirstSubstring(model, rule.Match) {
			return rule, true
		}
	}
	return ThinkingRule{}, false
}

// MatchFirstSubstring reports whether model contains any of the patterns
// (case-insensitive). Blank patterns are ignored.
func MatchFirstSubstring(model string, patterns []string) bool {
	lower := strings.ToLower(model)
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}
