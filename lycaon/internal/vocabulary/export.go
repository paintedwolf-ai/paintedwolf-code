package vocabulary

import (
	"sort"

	"github.com/lycaon/lycaon/internal/conditions"
)

// CatalogEntry is one row in schemas/workflow_vocabulary.json.
type CatalogEntry struct {
	ID            string `json:"id"`
	Layer         string `json:"layer"`
	Domain        string `json:"domain"`
	Status        string `json:"status"`
	Parameterized bool   `json:"parameterized"`
}

// ExportCatalog returns the machine-readable vocabulary catalog from shipped registry tables.
func ExportCatalog() []CatalogEntry {
	var out []CatalogEntry
	out = append(out,
		entry("gates_satisfied", "core", "—", "shipped", false),
		entry("gate_satisfied:*", "core", "—", "shipped", true),
	)
	for _, id := range shippedCoreConditions {
		out = append(out, entry(id, "core", "—", "shipped", false))
	}
	for _, id := range shippedCoreParameterized {
		out = append(out, entry(id, "core", "—", "shipped", true))
	}
	for _, id := range conditions.ShippedPlanDomainIDs() {
		out = append(out, entry(id, "domain", "plan", "shipped", false))
	}
	for _, id := range conditions.ShippedBlueprintDomainIDs() {
		out = append(out, entry(id, "domain", "blueprint", "shipped", false))
	}
	for _, id := range conditions.ShippedScanDomainIDs() {
		out = append(out, entry(id, "domain", "scan", "shipped", false))
	}
	for _, id := range conditions.ShippedHumanApprovalDomainIDs() {
		out = append(out, entry(id, "core", "—", "shipped", false))
	}
	for _, id := range conditions.ScanCatalogStubIDs() {
		out = append(out, entry(id, "domain", "scan", "catalog", false))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func entry(id, layer, domain, status string, parameterized bool) CatalogEntry {
	return CatalogEntry{
		ID:            id,
		Layer:         layer,
		Domain:        domain,
		Status:        status,
		Parameterized: parameterized,
	}
}

var shippedCoreConditions = []string{
	"workflow_active",
	"workflow_paused",
	"workflow_not_runnable",
	"posture_is_spec",
	"posture_is_build",
	"posture_is_orchestrate",
	"posture_is_vet",
	"posture_unresolved",
	"tool_is_state",
	"tool_is_delegation",
	"tool_is_handoff",
	"tool_is_task",
	"tool_is_write",
	"tool_is_command",
	"tool_is_scan_pack",
	"high_risk_tool",
	"stub_valid",
	"stub_invalid",
	"agent_is_plan_writer",
	"disallowed_agent",
	"doom_loop_exceeded",
	"delegation_active",
	"delegation_phase_setup",
	"delegation_phase_worker",
	"delegation_phase_done",
	"leg_pending",
	"all_legs_complete",
	"leg_failed",
	"worker_jobs_pending",
	"grounding_blocked",
	"completion_criteria_met",
	"file_modified_since_base",
	"permission_denied",
	"iteration_cap_near",
	"topology_stage_complete",
	"parallel_stages_complete",
	"delegation_closeout_complete",
	"delivery_gates_passed",
	"closeout_gates_passed",
	"orchestration_complete",
}

var shippedCoreParameterized = []string{
	"phase_is:*",
	"phase_skipped:*",
	"posture_is:*",
	"agent_is:*",
	"evidence_passed:*",
	"evidence_missing:*",
	"gate_passed:*",
	"user_feedback_pending:*",
	"user_feedback_received:*",
	"user_decision_pending:*",
	"user_decision_received:*",
	"user_decision:*",
	"hitl_consulted:*",
	"var_equals:*",
	"var_truthy:*",
}
