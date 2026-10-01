package prompts

// Playbook is a YAML-defined leg checklist.
type Playbook struct {
	ID          string           `yaml:"id"`
	Description string           `yaml:"description,omitempty"`
	Extends     string           `yaml:"extends,omitempty"`
	Triggers    PlaybookTriggers `yaml:"triggers"`
	Checklist   []string         `yaml:"checklist"`
	Fallback    bool             `yaml:"fallback,omitempty"`
}

// PlaybookTriggers selects when a playbook applies.
type PlaybookTriggers struct {
	TopologyPatterns []string `yaml:"topology_patterns"`
	PhaseIDs         []string `yaml:"phase_ids"`
}
