package workflow

import (
	"strings"

	"github.com/lycaon/lycaon/internal/conditions"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
)

// genericGateKitStaticLeaves are gate/complete_when ids custom workflows may compose.
var genericGateKitStaticLeaves = []string{
	workflowdef.CompleteWhenGatesSatisfied,
	"orchestration_complete",
	"child_run_complete",
	"child_run_failed",
	"choice_transition_required",
	"human_approval",
}

// genericGateKitLeafPrefixes are parameterized gate leaves custom workflows may compose.
var genericGateKitLeafPrefixes = []string{
	"var_equals:",
	"var_truthy:",
	"user_decision_received:",
	"user_feedback_received:",
	"user_decision:",
	"hitl_consulted:",
	"evidence_passed:",
	"phase_is:",
	"phase_skipped:",
	"gate_passed:",
	conditions.ObligationGatePrefix,
}

// internalDomainGateLeaves belong to bundled workflows.
var internalDomainGateLeaves = []string{
	"plan_stub_valid",
	"research_satisfied",
	"topology_stage_complete",
	"delegation_closeout_complete",
	"delivery_gates_passed",
	"closeout_gates_passed",
	"recon_or_board_ready",
	"worker_cycle_ready",
	"topology_report_delivered",
	"fanout_planned",
	"parallel_stages_complete",
	"implement_workflow_ready",
	"options_selection_valid",
}

// PublicGateKit returns custom-workflow and bundled-only gate identifiers.
func PublicGateKit() (static, prefixes, bundledOnly []string) {
	static = append([]string(nil), genericGateKitStaticLeaves...)
	prefixes = append([]string(nil), genericGateKitLeafPrefixes...)
	bundledOnly = append([]string(nil), internalDomainGateLeaves...)
	return static, prefixes, bundledOnly
}

// IsGenericGateKitLeaf reports whether id is in the supported custom-workflow gate vocabulary.
func IsGenericGateKitLeaf(id string) bool {
	id = strings.TrimSpace(id)
	if id == "" {
		return false
	}
	for _, leaf := range genericGateKitStaticLeaves {
		if id == leaf {
			return true
		}
	}
	for _, prefix := range genericGateKitLeafPrefixes {
		if strings.HasPrefix(id, prefix) && len(id) > len(prefix) {
			return true
		}
	}
	return false
}
