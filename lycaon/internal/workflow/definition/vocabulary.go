package definition

import (
	"strings"

	"github.com/lycaon/lycaon/internal/boolexpr"
	"github.com/lycaon/lycaon/internal/conditions"
)

// Built-in complete_when forms evaluated by GateEvaluator without the condition registry.
const (
	CompleteWhenGatesSatisfied = "gates_satisfied"
	CompleteWhenGateSatisfied  = "gate_satisfied:"
)

// registeredCompleteWhenPredicates are domain/core ids referenced from bundled manifests.
var registeredCompleteWhenPredicates = map[string]struct{}{
	"plan_stub_valid":              {},
	"options_selection_valid":      {},
	"research_satisfied":           {},
	"delegation_closeout_complete": {},
	"topology_stage_complete":      {},
	"parallel_stages_complete":     {},
	"implement_workflow_ready":     {},
	"plan_awaiting_approval":       {},
	"research_required":            {},
	"delivery_gates_passed":        {},
	"closeout_gates_passed":        {},
	"orchestration_complete":       {},
	"human_approval":               {},
}

// registeredGateLeafIDs are leaves allowed in phases[].gates and gate_satisfied: prefixes.
var registeredGateLeafIDs = map[string]struct{}{
	"delegation_closeout_complete": {},
	"plan_stub_valid":              {},
	"options_selection_valid":      {},
	"research_satisfied":           {},
	"delivery_gates_passed":        {},
	"closeout_gates_passed":        {},
	"evidence_passed:verify":       {},
	"evidence_passed:test":         {},
	"evidence_passed:security":     {},
	"evidence_missing:verify":      {},
	"evidence_missing:security":    {},
	"human_approval":               {},
	"recon_or_board_ready":         {},
	"worker_cycle_ready":           {},
	"topology_report_delivered":    {},
	"fanout_planned":               {},
	"child_run_complete":           {},
	"child_run_failed":             {},
	"choice_transition_required":   {},
}

// IsKnownGateLeaf reports whether id may appear in gates[] or gate_satisfied:<id>.
func IsKnownGateLeaf(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if _, ok := registeredGateLeafIDs[id]; ok {
		return true
	}
	return isParameterizedGateLeaf(id)
}

// IsKnownCompleteWhen validates a completion expression.
func IsKnownCompleteWhen(expr string) bool {
	expr = strings.TrimSpace(expr)
	if expr == "" || expr == CompleteWhenGatesSatisfied {
		return true
	}
	if strings.HasPrefix(expr, CompleteWhenGateSatisfied) {
		return IsKnownGateLeaf(strings.TrimPrefix(expr, CompleteWhenGateSatisfied))
	}
	if _, ok := registeredCompleteWhenPredicates[expr]; ok {
		return true
	}
	if _, ok := registeredGateLeafIDs[expr]; ok {
		return true
	}
	if isParameterizedGateLeaf(expr) {
		return true
	}
	if NeedsCompoundCompleteWhen(expr) {
		node, err := boolexpr.Parse(expr)
		if err != nil {
			return false
		}
		for _, id := range boolexpr.CollectIdents(node) {
			if !isKnownConditionLeaf(id) {
				return false
			}
		}
		return true
	}
	return false
}

func isKnownConditionLeaf(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	if _, ok := registeredCompleteWhenPredicates[id]; ok {
		return true
	}
	return IsKnownGateLeaf(id)
}

func NeedsCompoundCompleteWhen(expr string) bool {
	lower := strings.ToLower(expr)
	return strings.Contains(lower, " and ") ||
		strings.Contains(lower, " or ") ||
		strings.Contains(lower, " not ") ||
		strings.Contains(expr, "(")
}

func isParameterizedGateLeaf(id string) bool {
	prefixes := []string{
		"evidence_passed:",
		"evidence_missing:",
		"var_equals:",
		"var_truthy:",
		"user_feedback_received:",
		"user_decision_received:",
		"user_decision:",
		"hitl_consulted:",
		"phase_is:",
		"phase_skipped:",
		"gate_passed:",
		conditions.ObligationGatePrefix,
	}
	for _, p := range prefixes {
		if strings.HasPrefix(id, p) && len(id) > len(p) {
			return true
		}
	}
	return false
}

// CollectManifestVocabulary returns unique complete_when and gate leaf ids from a manifest.
func CollectManifestVocabulary(m Manifest) (completeWhen []string, gateLeaves []string) {
	seenCW := map[string]struct{}{}
	seenGate := map[string]struct{}{}
	for _, p := range m.PhaseDefs {
		if cw := strings.TrimSpace(p.CompleteWhen); cw != "" {
			if _, ok := seenCW[cw]; !ok {
				seenCW[cw] = struct{}{}
				completeWhen = append(completeWhen, cw)
			}
		}
		for _, g := range p.Gates {
			g = strings.TrimSpace(g)
			if g == "" {
				continue
			}
			if _, ok := seenGate[g]; !ok {
				seenGate[g] = struct{}{}
				gateLeaves = append(gateLeaves, g)
			}
		}
	}
	return completeWhen, gateLeaves
}
