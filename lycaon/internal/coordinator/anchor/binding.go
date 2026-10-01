package anchor

import "strings"

// Binding is one declarative inform/block/transform attachment on an Anchor.
type Binding struct {
	On       ID
	Selector Selector
	Effect   string
	When     string
	Render   string
	Tier     string
	// Invariants define template guarantees.
	Invariants BindingInvariants
}

// BindingInvariants are per-Binding template guarantees.
type BindingInvariants struct {
	TriggerClass string   `yaml:"trigger_class,omitempty" json:"trigger_class,omitempty"`
	FiresIn      []string `yaml:"fires_in,omitempty" json:"fires_in,omitempty"`
	Required     []string `yaml:"required,omitempty" json:"required,omitempty"`
	Forbidden    []string `yaml:"forbidden,omitempty" json:"forbidden,omitempty"`
	OptionalVars []string `yaml:"optional_vars,omitempty" json:"optional_vars,omitempty"`
}

// Selector is the shared Binding selector vocabulary (machine-state only).
type Selector struct {
	Surface        string   `yaml:"surface,omitempty" json:"surface,omitempty"`
	Phase          *string  `yaml:"phase,omitempty" json:"phase,omitempty"`
	Workflow       *string  `yaml:"workflow,omitempty" json:"workflow,omitempty"`
	Tool           *string  `yaml:"tool,omitempty" json:"tool,omitempty"`
	Tools          []string `yaml:"tools,omitempty" json:"tools,omitempty"`
	Profiles       []string `yaml:"profiles,omitempty" json:"profiles,omitempty"`
	Surfaces       []string `yaml:"surfaces,omitempty" json:"surfaces,omitempty"`
	SessionPosture []string `yaml:"session_posture,omitempty" json:"session_posture,omitempty"`
}

// MatchContext is the typed emit-time selector fact set.
type MatchContext struct {
	Surface        string
	Phase          string
	Workflow       string
	Tool           string
	Profile        string
	SessionPosture string
	SessionID      string
}

// Matches reports whether sel accepts ctx.
func (s Selector) Matches(ctx MatchContext) bool {
	if surf := strings.TrimSpace(s.Surface); surf != "" && surf != strings.TrimSpace(ctx.Surface) {
		return false
	}
	if s.Phase != nil {
		want := strings.TrimSpace(*s.Phase)
		if want != "" && want != strings.TrimSpace(ctx.Phase) {
			return false
		}
	}
	if s.Workflow != nil {
		want := strings.TrimSpace(*s.Workflow)
		if want != "" && want != strings.TrimSpace(ctx.Workflow) {
			return false
		}
	}
	if s.Tool != nil && strings.TrimSpace(*s.Tool) != "" && *s.Tool != strings.TrimSpace(ctx.Tool) {
		return false
	}
	if !matchesOne(s.Tools, ctx.Tool) {
		return false
	}
	if !matchesOne(s.Profiles, ctx.Profile) {
		return false
	}
	if !matchesOne(s.Surfaces, ctx.Surface) {
		return false
	}
	if !matchesOne(s.SessionPosture, ctx.SessionPosture) {
		return false
	}
	return true
}

func matchesOne(wants []string, actual string) bool {
	if len(wants) == 0 {
		return true
	}
	actual = strings.TrimSpace(actual)
	for _, want := range wants {
		if strings.TrimSpace(want) == actual && actual != "" {
			return true
		}
	}
	return false
}

// IsInform reports effect:inform.
func (b *Binding) IsInform() bool {
	return b != nil && strings.EqualFold(strings.TrimSpace(b.Effect), "inform")
}
