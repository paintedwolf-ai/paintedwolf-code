package rules

import (
	"fmt"
	"sort"
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
)

// WhenLeaf is one condition a rule's when map names, carrying the polarity the
// map's value declares.
type WhenLeaf struct {
	Name    string
	Negated bool
}

// String renders the leaf as boolean-expression source.
func (l WhenLeaf) String() string {
	if l.Negated {
		return "not " + l.Name
	}
	return l.Name
}

// CanonicalWhenLeaves resolves a when map to its ordered condition leaves, the form
// evaluation uses: a leaf name carries a parameterized value verbatim
// (var_equals:plan.status,draft), which rendered text cannot round-trip.
func CanonicalWhenLeaves(when map[string]any) ([]WhenLeaf, error) {
	if len(when) == 0 {
		return nil, fmt.Errorf("empty when map")
	}
	leaves := make([]WhenLeaf, 0, len(when))
	for key, raw := range when {
		switch key {
		case "posture_is":
			val, _ := raw.(string)
			val = strings.TrimSpace(val)
			if val == "" {
				return nil, fmt.Errorf("posture_is requires value")
			}
			leaves = append(leaves, WhenLeaf{Name: "posture_is_" + val})
		case "posture_unresolved", "tool_is_state", "tool_is_delegation", "tool_is_task",
			"tool_is_write", "high_risk_tool", "stub_valid", "stub_invalid",
			"agent_is_plan_writer", "disallowed_agent":
			// Presence-only keys: a falsy value declares nothing rather than the
			// negation, so it contributes no leaf.
			if truthy(raw) {
				leaves = append(leaves, WhenLeaf{Name: key})
			}
		default:
			if conditions.IsHostOnlyCondition(key) {
				return nil, fmt.Errorf("host-only when key %q", key)
			}
			leaves = append(leaves, WhenLeaf{Name: key, Negated: !truthy(raw)})
		}
	}
	sort.Slice(leaves, func(i, j int) bool { return leaves[i].String() < leaves[j].String() })
	return leaves, nil
}

// CanonicalWhenString renders a when map as boolean-expression source for
// display, keying, and diagnostics. Evaluation uses CanonicalWhenLeaves.
func CanonicalWhenString(when map[string]any) (string, error) {
	leaves, err := CanonicalWhenLeaves(when)
	if err != nil {
		return "", err
	}
	parts := make([]string, 0, len(leaves))
	for _, leaf := range leaves {
		parts = append(parts, leaf.String())
	}
	return strings.Join(parts, " and "), nil
}
