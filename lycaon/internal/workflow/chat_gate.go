package workflow

import (
	"strings"
)

// SatisfyGateInVars marks a registered complete_when / gates[] leaf satisfied in scaffold vars.
func SatisfyGateInVars(vars map[string]any, gate string) map[string]any {
	gate = strings.TrimSpace(gate)
	if gate == "" {
		return vars
	}
	switch gate {
	case "human_approval":
		vars = SetHumanApprovalIssued(vars, true)
		return SetGateSatisfied(vars, gate, true)
	default:
		return SetGateSatisfied(vars, gate, true)
	}
}
