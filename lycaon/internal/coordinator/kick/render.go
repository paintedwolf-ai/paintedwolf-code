package kick

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
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
	if live.PendingOverlays > 0 {
		data["pending_overlays"] = live.PendingOverlays
	}
	if live.PartialWorkers > 0 {
		data["partial_workers"] = live.PartialWorkers
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
	if meta.workerRoundNotice != nil {
		meta.workerRoundNotice.renderData(data)
	}
	for k, v := range meta.vars {
		data[k] = v
	}
	return data
}

func (k *KickEngine) resolveHintTarget(ctx context.Context, sessionID, kickID string, target kickRenderTarget) (string, bool, error) {
	switch target.kind {
	case renderTargetHint:
		code := strings.TrimSpace(target.hintCode)
		if code == "" {
			return "", true, fmt.Errorf("kick %q: empty hint code", kickID)
		}
		if target.resolveHint == nil {
			return code, true, nil
		}
		text, _ := target.resolveHint(code)
		if strings.TrimSpace(text) == "" {
			return code, true, nil
		}
		return strings.TrimSpace(text), true, nil
	case renderTargetTemplate:
		return "", false, nil
	default:
		fn := k.stemHints.Load()
		if fn == nil || *fn == nil {
			fn = defaultStemHints.Load()
		}
		if fn != nil && *fn != nil {
			if text, isHint, _ := (*fn)(ctx, sessionID, strings.TrimSpace(kickID)); isHint {
				return strings.TrimSpace(text), true, nil
			}
		}
		return "", false, nil
	}
}

func (k *KickEngine) renderCoordinatorKick(ctx context.Context, sessionID, kickID string, meta kickMeta, live CoordinatorKickRenderContext) (string, error) {
	if text, handled, err := k.resolveHintTarget(ctx, sessionID, kickID, meta.renderTarget); handled {
		return text, err
	}
	cfg := k.config()
	if cfg.engine == nil {
		return "", fmt.Errorf("kick %q: prompt engine not configured", kickID)
	}
	data := buildKickRenderData(meta, live, time.Now().UTC())
	reader, err := kickReader(live)
	if err != nil {
		return "", fmt.Errorf("kick reader: %w", err)
	}
	if err := prompts.MergeCoordinatorKickVarsForTools(data, reader); err != nil {
		return "", fmt.Errorf("kick policy vars: %w", err)
	}
	return cfg.engine.RenderKick(ctx, strings.TrimSpace(kickID), data)
}

func kickReader(live CoordinatorKickRenderContext) (prompts.Reader, error) {
	profiles, err := sandbox.LoadToolProfiles()
	if err != nil {
		return prompts.Reader{}, err
	}
	surfaceID := strings.TrimSpace(live.SurfaceID)
	plan := live.ToolPlan
	if !plan.Compiled() && surfaceID != "" {
		plan, err = prompts.CompileSurfacePlan(surfaceID, max(live.RootCount, 1))
		if err != nil {
			return prompts.Reader{}, err
		}
	}
	var loaded map[string]bool
	if plan.Compiled() {
		loaded = make(map[string]bool, len(plan.AddressableNames()))
		for _, name := range plan.AddressableNames() {
			loaded[name] = true
		}
	}
	return prompts.NewReader(prompts.ReaderInput{
		ProfileID: prompts.CoordinatorProfileID,
		SurfaceID: surfaceID,
		Profiles:  profiles,
		Plan:      plan,
		Loaded:    loaded,
		RootCount: live.RootCount,
		WebSearch: true,
	})
}
