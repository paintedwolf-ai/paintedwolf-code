package anchor

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// ErrWorkflowVersionMissing is returned when MatchContext names a workflow but lacks a workflow version.
var ErrWorkflowVersionMissing = errors.New("anchor: match context specifies workflow without workflow version")

// RunSource identifies an active run's workflow identity.
type RunSource interface {
	WorkflowIdentity() (workflowID, workflowVersion string)
}

// RunMatch creates a typed MatchContext for an active run.
func RunMatch(run RunSource, surface, phase string) MatchContext {
	if run == nil {
		return MatchContext{Surface: surface, Phase: phase}
	}
	wf, ver := run.WorkflowIdentity()
	return MatchContext{
		Surface:         surface,
		Phase:           phase,
		Workflow:        strings.TrimSpace(wf),
		WorkflowVersion: strings.TrimSpace(ver),
	}
}

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

	workflowVersion string
}

// WorkflowVersion returns the bound workflow version, or empty for non-workflow bindings.
func (b *Binding) WorkflowVersion() string {
	if b == nil {
		return ""
	}
	return b.workflowVersion
}

// IsWorkflowTier reports whether b is a workflow-tier binding.
func (b *Binding) IsWorkflowTier() bool {
	return b != nil && strings.EqualFold(strings.TrimSpace(b.Tier), "workflow")
}

// Matches reports whether b matches ctx, enforcing exact workflow version equality for workflow-tier bindings.
func (b *Binding) Matches(ctx MatchContext) bool {
	if b == nil {
		return false
	}
	if !b.Selector.Matches(ctx) {
		return false
	}
	if b.IsWorkflowTier() {
		return strings.TrimSpace(b.workflowVersion) == strings.TrimSpace(ctx.WorkflowVersion)
	}
	return true
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

// UnmarshalYAML rejects selector.workflow_version so third-party YAML cannot author ad-hoc version selectors.
func (s *Selector) UnmarshalYAML(value *yaml.Node) error {
	type rawSelector Selector
	var raw rawSelector
	if err := value.Decode(&raw); err != nil {
		return err
	}
	for i := 0; i < len(value.Content)-1; i += 2 {
		if strings.TrimSpace(value.Content[i].Value) == "workflow_version" {
			return fmt.Errorf("selector.workflow_version is forbidden; workflow versions are set by the host manifest")
		}
	}
	*s = Selector(raw)
	return nil
}

// MatchContext is the typed emit-time selector fact set.
type MatchContext struct {
	Surface         string
	Phase           string
	Workflow        string
	WorkflowVersion string
	Tool            string
	Profile         string
	SessionPosture  string
	SessionID       string
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
