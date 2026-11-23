package vocabulary

const (
	CompleteWhenGatesSatisfied = "gates_satisfied"
	CompleteWhenGateSatisfied  = "gate_satisfied:"
)

type Usage uint8

const (
	Predicate Usage = iota
	OverlayPredicate
	CompletionForm
	CatalogOnly
	HostOnly
)

type Declaration struct {
	Domain string
	Usage  Usage
}

var declarations = map[string]Declaration{
	"var_set:*":                     {"—", HostOnly},
	"agent_is:*":                    {"—", Predicate},
	"agent_is_plan_writer":          {"—", Predicate},
	"approval_denied":               {"—", Predicate},
	"closeout_gates_passed":         {"—", Predicate},
	"critic_task":                   {"—", Predicate},
	"delegation_closeout_complete":  {"—", OverlayPredicate},
	"delivery_gates_passed":         {"—", Predicate},
	"disallowed_agent":              {"—", Predicate},
	"doom_loop_exceeded":            {"—", Predicate},
	"evidence_missing:*":            {"—", Predicate},
	"fanout_planned":                {"—", Predicate},
	"grounding_blocked":             {"—", Predicate},
	"handoff_not_ready":             {"—", Predicate},
	"high_risk_tool":                {"—", Predicate},
	"iteration_cap_near":            {"—", Predicate},
	"pack_runner_task":              {"—", Predicate},
	"parallel_stages_complete":      {"—", Predicate},
	"plan_not_approved":             {"—", Predicate},
	"posture_is:*":                  {"—", Predicate},
	"posture_is_build":              {"—", Predicate},
	"posture_is_orchestrate":        {"—", Predicate},
	"posture_is_spec":               {"—", Predicate},
	"posture_is_vet":                {"—", Predicate},
	"posture_unresolved":            {"—", Predicate},
	"project_has_no_roots":          {"—", Predicate},
	"project_has_roots":             {"—", Predicate},
	"recon_or_board_ready":          {"—", Predicate},
	"research_task":                 {"—", Predicate},
	"session_is_agent_mode":         {"—", Predicate},
	"stub_invalid":                  {"—", Predicate},
	"stub_valid":                    {"—", Predicate},
	"tool_is_command":               {"—", Predicate},
	"tool_is_delegation":            {"—", Predicate},
	"tool_is_handoff":               {"—", Predicate},
	"tool_is_scan_pack":             {"—", Predicate},
	"tool_is_state":                 {"—", Predicate},
	"tool_is_task":                  {"—", Predicate},
	"tool_is_workflow_advance":      {"—", Predicate},
	"tool_is_workflow_transition":   {"—", Predicate},
	"tool_is_write":                 {"—", Predicate},
	"tool_names_agent":              {"—", Predicate},
	"tool_requires_project_roots":   {"—", Predicate},
	"topology_report_delivered":     {"—", Predicate},
	"topology_stage_complete":       {"—", Predicate},
	"user_decision_pending:*":       {"—", Predicate},
	"user_feedback_pending:*":       {"—", Predicate},
	"worker_cycle_ready":            {"—", Predicate},
	"workflow_active":               {"—", Predicate},
	"workflow_not_runnable":         {"—", Predicate},
	"workflow_paused":               {"—", Predicate},
	"baseline_scan_complete":        {"scan", Predicate},
	"lint_gate_passed":              {"scan", Predicate},
	"no_critical_findings":          {"scan", Predicate},
	"required_scans_enqueued":       {"scan", Predicate},
	"scan_pending":                  {"scan", Predicate},
	"scan_stale":                    {"scan", Predicate},
	"security_evidence_fresh":       {"scan", Predicate},
	"blueprint_materialized":        {"blueprint", Predicate},
	"options_selection_valid":       {"blueprint", Predicate},
	"breaking_missing":              {"plan", Predicate},
	"implement_workflow_ready":      {"plan", Predicate},
	"plan_awaiting_approval":        {"plan", Predicate},
	"plan_stub_valid":               {"plan", Predicate},
	"research_required":             {"plan", Predicate},
	"research_satisfied":            {"plan", Predicate},
	"review_depth_missing":          {"plan", Predicate},
	"review_loop_active":            {"plan", Predicate},
	"scope_missing":                 {"plan", Predicate},
	"child_run_complete":            {"—", OverlayPredicate},
	"child_run_failed":              {"—", OverlayPredicate},
	"choice_transition_required":    {"—", OverlayPredicate},
	"evidence_passed:*":             {"—", OverlayPredicate},
	"gate_passed:*":                 {"—", OverlayPredicate},
	"hitl_consulted:*":              {"—", OverlayPredicate},
	"human_approval":                {"—", OverlayPredicate},
	"obligation_settled:*":          {"—", OverlayPredicate},
	"orchestration_complete":        {"—", OverlayPredicate},
	"phase_is:*":                    {"—", OverlayPredicate},
	"phase_skipped:*":               {"—", OverlayPredicate},
	"user_decision:*":               {"—", OverlayPredicate},
	"user_decision_received:*":      {"—", OverlayPredicate},
	"user_feedback_received:*":      {"—", OverlayPredicate},
	"var_equals:*":                  {"—", OverlayPredicate},
	"var_truthy:*":                  {"—", OverlayPredicate},
	"findings_triaged":              {"scan", CatalogOnly},
	"remediation_complete":          {"scan", CatalogOnly},
	CompleteWhenGateSatisfied + "*": {"—", CompletionForm},
	CompleteWhenGatesSatisfied:      {"—", CompletionForm},
	"permission_denied":             {"—", CatalogOnly},
}
