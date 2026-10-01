package kick

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/prompts"
)

func buildKickRenderData(meta kickMeta, live CoordinatorKickRenderContext, now time.Time) map[string]any {
	data := map[string]any{}
	if live.WorkflowID != "" {
		data["workflow_id"] = live.WorkflowID
	}
	if live.CurrentPhase != "" {
		data["current_phase"] = live.CurrentPhase
	}
	if meta.completedAt != nil {
		if rel := FormatKickRelativeAgo(now, *meta.completedAt); rel != "" {
			data["completed_ago"] = rel
		}
	}
	if meta.workerDigest != "" {
		data["worker_digest"] = meta.workerDigest
	}
	if len(meta.workerDecisionRequest) > 0 {
		data["last_worker_decision_request"] = meta.workerDecisionRequest
	}
	if meta.evidenceDigest != "" {
		data["evidence_digest"] = meta.evidenceDigest
	}
	if meta.commandCompletion != "" {
		data["command_completion"] = meta.commandCompletion
	}
	if meta.commandRefusal != "" {
		data["command_refusal"] = meta.commandRefusal
	}
	if meta.topologyOutput != "" {
		data["topology_output"] = meta.topologyOutput
	}
	if meta.designForkCriterion != "" {
		data["options_criterion"] = meta.designForkCriterion
	}
	if meta.fanoutPlanText != "" {
		data["fanout_plan"] = meta.fanoutPlanText
	}
	if meta.maxFanoutLegs > 0 {
		data["max_fanout_legs"] = meta.maxFanoutLegs
	}
	if live.BatchPhase != "" {
		data["batch_phase"] = live.BatchPhase
	}
	if len(live.PendingOverlayJobs) > 0 {
		data["pending_overlay_jobs"] = live.PendingOverlayJobs
	}
	if len(live.PartialWorkerJobs) > 0 {
		data["partial_worker_jobs"] = live.PartialWorkerJobs
	}
	if len(meta.promotedPaths) > 0 {
		data["promoted_paths"] = append([]string(nil), meta.promotedPaths...)
	}
	if live.AdvanceWhenGateMet != "" {
		data["advance_when_gate_met"] = live.AdvanceWhenGateMet
	}
	if live.ProgressClosureArmed && live.ProgressOpenItems > 0 {
		data["progress_closure_armed"] = true
		data["progress_open_items"] = live.ProgressOpenItems
	}
	if len(live.FailedLeaves) > 0 {
		data["failed_leaves"] = append([]string(nil), live.FailedLeaves...)
	}
	if len(live.GateObligations) > 0 {
		rows := make([]map[string]any, len(live.GateObligations))
		for i, o := range live.GateObligations {
			rows[i] = map[string]any{
				"id":       o.ID,
				"purpose":  o.Purpose,
				"satisfy":  append([]string(nil), o.Satisfy...),
				"missing":  append([]string(nil), o.Missing...),
				"required": append([]string(nil), o.Required...),
			}
		}
		data["gate_obligations"] = rows
	}
	if meta.scanID != "" {
		data["scan_id"] = meta.scanID
		data["scan_status"] = meta.scanStatus
		data["scan_categories"] = meta.scanCategories
		data["scan_findings_count"] = meta.scanFindingsCount
	}
	if meta.workerBudget != nil {
		meta.workerBudget.renderData(data)
	}
	for k, v := range meta.vars {
		data[k] = v
	}
	return data
}

func (k *KickEngine) renderCoordinatorKick(ctx context.Context, kickID string, meta kickMeta, live CoordinatorKickRenderContext) (string, error) {
	cfg := k.config()
	if cfg.engine == nil {
		return "", fmt.Errorf("kick %q: prompt engine not configured", kickID)
	}
	data := buildKickRenderData(meta, live, time.Now().UTC())
	if err := prompts.MergeCoordinatorKickPolicyVars(data); err != nil {
		return "", fmt.Errorf("kick policy vars: %w", err)
	}
	return cfg.engine.RenderKick(ctx, strings.TrimSpace(kickID), data)
}
