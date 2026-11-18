package conditionstest

// Expected condition identifiers for catalog and registry assertions.
var (
	BlueprintIDs     = []string{"blueprint_materialized", "options_selection_valid"}
	HumanApprovalIDs = []string{"human_approval"}
	PlanIDs          = []string{
		"plan_stub_valid",
		"plan_awaiting_approval",
		"research_required",
		"research_satisfied",
		"review_loop_active",
		"scope_missing",
		"breaking_missing",
		"review_depth_missing",
		"implement_workflow_ready",
	}
	ScanIDs = []string{
		"scan_pending",
		"scan_stale",
		"required_scans_enqueued",
		"lint_gate_passed",
		"baseline_scan_complete",
		"security_evidence_fresh",
		"no_critical_findings",
	}
	DeferredScanIDs = []string{
		"findings_triaged",
		"remediation_complete",
	}
	DelegateAndSubroutineIDs = []string{"recon_or_board_ready", "worker_cycle_ready", "topology_report_delivered", "fanout_planned", "child_run_complete", "child_run_failed"}
)
