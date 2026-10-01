package skills

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Selector defines the skills exposed to one agent. All is open-world over the
// effective catalog; Names is a closed exact-name set.
type Selector struct {
	All   bool
	Names []string
}

// UnmarshalYAML accepts either the scalar "all" or a non-empty sequence of
// skill names. Omission is the only spelling for no skills.
func (s *Selector) UnmarshalYAML(node *yaml.Node) error {
	if s == nil {
		return fmt.Errorf("skills: nil selector")
	}
	*s = Selector{}
	switch node.Kind {
	case yaml.ScalarNode:
		if strings.TrimSpace(node.Value) != "all" {
			return fmt.Errorf("skills: scalar must be %q", "all")
		}
		s.All = true
		return nil
	case yaml.SequenceNode:
		if len(node.Content) == 0 {
			return fmt.Errorf("skills: omit the field instead of declaring an empty list")
		}
		seen := make(map[string]struct{}, len(node.Content))
		for _, child := range node.Content {
			if child.Kind != yaml.ScalarNode {
				return fmt.Errorf("skills: entries must be names")
			}
			name := strings.TrimSpace(child.Value)
			if !ValidName(name) {
				return fmt.Errorf("skills: invalid skill name %q", name)
			}
			if _, duplicate := seen[name]; duplicate {
				return fmt.Errorf("skills: duplicate skill name %q", name)
			}
			seen[name] = struct{}{}
			s.Names = append(s.Names, name)
		}
		return nil
	default:
		return fmt.Errorf("skills: expected %q or a name list", "all")
	}
}

// Empty reports whether an agent exposes no skills.
func (s Selector) Empty() bool {
	return !s.All && len(s.Names) == 0
}

// Includes reports whether a catalog skill belongs to this selector.
func (s Selector) Includes(name string) bool {
	if s.All {
		return true
	}
	for _, candidate := range s.Names {
		if candidate == name {
			return true
		}
	}
	return false
}

// Select preserves effective-catalog order while applying this selector.
func (s Selector) Select(catalog []Skill) []Skill {
	if s.All {
		return catalog
	}
	selected := make([]Skill, 0, len(s.Names))
	for _, skill := range catalog {
		if s.Includes(skill.Name) {
			selected = append(selected, skill)
		}
	}
	return selected
}
