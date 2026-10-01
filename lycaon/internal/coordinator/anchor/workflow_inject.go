package anchor

import (
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/anchorcatalog"
	"gopkg.in/yaml.v3"
)

// WorkflowInject is a workflow-tier Anchor Binding authored in workflow.yaml.
type WorkflowInject struct {
	On       string    `yaml:"on" json:"on"`
	Selector Selector  `yaml:"selector" json:"selector"`
	Effect   string    `yaml:"effect" json:"effect"`
	When     string    `yaml:"when,omitempty" json:"when,omitempty"`
	Render   string    `yaml:"render,omitempty" json:"render,omitempty"`
	Tier     string    `yaml:"tier,omitempty" json:"tier,omitempty"`
	Dedup    yaml.Node `yaml:"dedup,omitempty" json:"-"`
}

// Binding validates and binds the inject to its containing workflow.
func (in WorkflowInject) Binding(workflowID string) (*Binding, error) {
	on := ID(strings.TrimSpace(in.On))
	if on == "" {
		return nil, fmt.Errorf("inject missing on")
	}
	effect := strings.TrimSpace(in.Effect)
	if effect == "" {
		return nil, fmt.Errorf("inject on %s: missing effect", on)
	}
	tier := strings.TrimSpace(in.Tier)
	if tier == "" {
		tier = "workflow"
	}
	if !strings.EqualFold(tier, "workflow") {
		return nil, fmt.Errorf("inject on %s: tier must be workflow (got %q)", on, tier)
	}
	b := &Binding{
		On: on, Selector: in.Selector, Effect: effect,
		When: strings.TrimSpace(in.When), Render: strings.TrimSpace(in.Render), Tier: "workflow",
	}
	if b.IsInform() && b.Render == "" {
		return nil, fmt.Errorf("inject on %s: inform requires render", on)
	}
	wf := strings.TrimSpace(workflowID)
	if wf != "" && (b.Selector.Workflow == nil || strings.TrimSpace(*b.Selector.Workflow) == "") {
		b.Selector.Workflow = &wf
	}
	if b.Selector.Workflow == nil || strings.TrimSpace(*b.Selector.Workflow) == "" {
		return nil, fmt.Errorf("inject on %s: selector.workflow required for workflow-tier bindings", on)
	}
	return b, nil
}

type workflowInjectDocument struct {
	ID      string           `yaml:"id"`
	Injects []WorkflowInject `yaml:"injects"`
}

func (r *Registry) loadWorkflowInjects(unitID string, content []byte) error {
	var doc workflowInjectDocument
	if err := yaml.Unmarshal(content, &doc); err != nil {
		return fmt.Errorf("%s: %w", unitID, err)
	}
	workflowID := strings.TrimSpace(doc.ID)
	if workflowID == "" {
		return fmt.Errorf("%s: workflow id required", unitID)
	}
	for i, inject := range doc.Injects {
		b, err := inject.Binding(workflowID)
		if err != nil {
			return fmt.Errorf("%s injects[%d]: %w", unitID, i, err)
		}
		if err := anchorcatalog.Require(string(b.On)); err != nil {
			return fmt.Errorf("%s injects[%d]: %w", unitID, i, err)
		}
		if r.loader != nil && b.When != "" {
			if err := r.loader.ValidateCondition(b.When); err != nil {
				return fmt.Errorf("%s injects[%d] when: %w", unitID, i, err)
			}
		}
		r.add(b)
	}
	return nil
}
