package detectionpack

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// FixtureCase is one corpus action. YAML may be a bare command string or an
// object carrying the same tool name and arguments the production gate receives.
type FixtureCase struct {
	Tool             string
	Command          string
	Args             map[string]any
	ApprovalCategory string
	ApprovalSubject  string
	// TargetFiles are the paths the action names, home-relative as the event
	// projection produces them. Required to exercise any rule matching TargetFile.
	TargetFiles []string
}

// UnmarshalYAML accepts a scalar command string or a production-shaped tool action.
func (c *FixtureCase) UnmarshalYAML(value *yaml.Node) error {
	if c == nil {
		return fmt.Errorf("nil FixtureCase")
	}
	switch value.Kind {
	case yaml.AliasNode:
		return c.UnmarshalYAML(value.Alias)
	case yaml.ScalarNode:
		c.Command = value.Value
		return nil
	case yaml.MappingNode:
		var raw struct {
			Tool             string         `yaml:"tool"`
			Command          string         `yaml:"command"`
			Args             map[string]any `yaml:"args"`
			ApprovalCategory string         `yaml:"approval_category"`
			ApprovalSubject  string         `yaml:"approval_subject"`
			TargetFiles      []string       `yaml:"target_files"`
		}
		if err := value.Decode(&raw); err != nil {
			return err
		}
		if raw.Command == "" && len(raw.Args) == 0 {
			return fmt.Errorf("fixture object requires command or args")
		}
		c.Tool = raw.Tool
		c.Command = raw.Command
		c.Args = raw.Args
		c.ApprovalCategory = raw.ApprovalCategory
		c.ApprovalSubject = raw.ApprovalSubject
		c.TargetFiles = raw.TargetFiles
		return nil
	default:
		return fmt.Errorf("fixture must be a string or object")
	}
}
