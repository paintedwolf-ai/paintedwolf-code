package contribution

import (
	"fmt"
	"strings"
)

// Condition is a structured predicate tree.
type Condition struct {
	All  []Condition `yaml:"all,omitempty" json:"all,omitempty"`
	Any  []Condition `yaml:"any,omitempty" json:"any,omitempty"`
	Not  *Condition  `yaml:"not,omitempty" json:"not,omitempty"`
	Fact string      `yaml:"fact,omitempty" json:"fact,omitempty"`
	// Is is the fact operand.
	Is string `yaml:"is,omitempty" json:"is,omitempty"`
}

// conditionScope resolves referenced declarations.
type conditionScope struct {
	Provider          string
	RequirementExists func(ID) bool
	// BooleanConfiguration reports declaration and type validity.
	BooleanConfiguration func(ID) (declared, boolean bool)
}

// validateCondition types the tree against the closed fact vocabulary.
func validateCondition(c Condition, scope conditionScope) error {
	set := 0
	if len(c.All) > 0 {
		set++
	}
	if len(c.Any) > 0 {
		set++
	}
	if c.Not != nil {
		set++
	}
	if c.Fact != "" {
		set++
	}
	if set != 1 {
		return fmt.Errorf("condition node must be exactly one of all, any, not, or fact")
	}
	if c.Is != "" && c.Fact == "" {
		return fmt.Errorf("is requires a fact node")
	}
	switch {
	case len(c.All) > 0:
		for _, child := range c.All {
			if err := validateCondition(child, scope); err != nil {
				return err
			}
		}
	case len(c.Any) > 0:
		for _, child := range c.Any {
			if err := validateCondition(child, scope); err != nil {
				return err
			}
		}
	case c.Not != nil:
		return validateCondition(*c.Not, scope)
	default:
		return validateFactNode(c, scope)
	}
	return nil
}

func validateFactNode(c Condition, scope conditionScope) error {
	spec, ok := facts[c.Fact]
	if !ok {
		return fmt.Errorf("unknown fact %q", c.Fact)
	}
	if spec.Operand == OperandNone {
		if c.Is != "" {
			return fmt.Errorf("fact %s takes no operand", c.Fact)
		}
		return nil
	}
	operand := strings.TrimSpace(c.Is)
	if operand == "" {
		return fmt.Errorf("fact %s requires an operand (is:)", c.Fact)
	}
	switch spec.Operand {
	case OperandLanguage:
		if !supportedLanguageSet[operand] {
			return fmt.Errorf("fact %s: unknown language %q", c.Fact, operand)
		}
	case OperandRequirement:
		id, err := ParseID(operand)
		if err != nil {
			return fmt.Errorf("fact %s: %w", c.Fact, err)
		}
		if scope.Provider != "" && id.Provider != scope.Provider {
			return fmt.Errorf("fact %s: requirement %s must belong to the declaring pack", c.Fact, operand)
		}
		if scope.RequirementExists != nil && !scope.RequirementExists(id) {
			return fmt.Errorf("fact %s: requirement %s is not declared", c.Fact, operand)
		}
	case OperandConfiguration:
		id, err := ParseID(operand)
		if err != nil {
			return fmt.Errorf("fact %s: %w", c.Fact, err)
		}
		if scope.Provider != "" && id.Provider != scope.Provider {
			return fmt.Errorf(
				"fact %s: configuration %s belongs to %s — a pack conditions on its own settings only",
				c.Fact, operand, id.Provider)
		}
		if scope.BooleanConfiguration != nil {
			declared, boolean := scope.BooleanConfiguration(id)
			if !declared {
				return fmt.Errorf("fact %s: configuration %s is not declared", c.Fact, operand)
			}
			if !boolean {
				return fmt.Errorf("fact %s: configuration %s is not a boolean property", c.Fact, operand)
			}
		}
	case OperandShellView:
		if !shellViews[operand] {
			return fmt.Errorf("fact %s: unknown shell view %q", c.Fact, operand)
		}
	case OperandRegion:
		if !focusRegions[operand] {
			return fmt.Errorf("fact %s: unknown region %q", c.Fact, operand)
		}
	case OperandWorkspace:
		if !workspaceKinds[operand] {
			return fmt.Errorf("fact %s: unknown workspace kind %q", c.Fact, operand)
		}
	case OperandFeature:
		if !contextFeatures[operand] {
			return fmt.Errorf("fact %s: unknown context feature %q", c.Fact, operand)
		}
	case OperandNone:
	}
	return nil
}

// conditionUsesHostFacts reports whether any node binds a host-plane fact.
func conditionUsesHostFacts(c *Condition) bool {
	if c == nil {
		return false
	}
	if c.Fact != "" {
		return facts[c.Fact].Plane == PlaneHost
	}
	for _, child := range c.All {
		if conditionUsesHostFacts(&child) {
			return true
		}
	}
	for _, child := range c.Any {
		if conditionUsesHostFacts(&child) {
			return true
		}
	}
	return conditionUsesHostFacts(c.Not)
}
