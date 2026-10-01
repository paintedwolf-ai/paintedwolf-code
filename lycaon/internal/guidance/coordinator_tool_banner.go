package guidance

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/lycaon/lycaon/internal/hostmarker"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
	"github.com/lycaon/lycaon/internal/spawn"
	"github.com/lycaon/lycaon/internal/tooloutput"
	"github.com/lycaon/lycaon/pkg/api"
)

// shortIDBytes bounds an id rendered inline in a banner.
const shortIDBytes = 4

// WorkerRosterLine is one in-flight worker row for enrich/worker-inflight-roster.md.
type WorkerRosterLine struct {
	JobPrefix    string
	Agent        string
	Status       string
	ScopeSummary string
	TouchSummary string
}

// TaskQueuedBannerOpts are the inputs to enrich/task-queued.md.
type TaskQueuedBannerOpts struct {
	AgentType   string
	JobID       string
	MaxInFlight int
}

// RenderTaskQueuedBanner renders enrich/task-queued.md after a successful task() enqueue.
func RenderTaskQueuedBanner(ctx context.Context, opts TaskQueuedBannerOpts) (string, error) {
	maxInFlight := opts.MaxInFlight
	if maxInFlight <= 0 {
		maxInFlight = spawn.MaxInFlightTaskWorkers
	}
	block, err := RenderGuidance(ctx, "enrich/task-queued", map[string]any{
		"agent_type":    strings.TrimSpace(opts.AgentType),
		"job_id":        strings.TrimSpace(opts.JobID),
		"max_in_flight": maxInFlight,
	})
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" || !strings.Contains(block, "BANNER_TASK_QUEUED") {
		return "", fmt.Errorf("task-queued banner missing BANNER_TASK_QUEUED")
	}
	return "\n" + block, nil
}

// RenderTakeBranchOverlapBanner appends guidance for overlapping jobs.
func RenderTakeBranchOverlapBanner(ctx context.Context, mergeOutput string) (string, error) {
	result, ok := parseWorkerMergeResultOutput(mergeOutput)
	if !ok || len(result.OverlapJobIDs) == 0 {
		return "", nil
	}
	if result.PromoteOrder != "" && result.PromoteOrder != api.WorkerPromoteOrderIndependent {
		return "", nil
	}
	block, err := RenderGuidance(ctx, "enrich/take-branch-overlap", takeBranchOverlapContext(result))
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" || !strings.Contains(block, "BANNER_TAKE_BRANCH_OVERLAP") {
		return "", fmt.Errorf("take-branch-overlap banner missing code")
	}
	return "\n" + block, nil
}

func takeBranchOverlapContext(result api.WorkerMergeResult) map[string]any {
	paths := conflictPathsFromResult(result)
	cleanPreview := len(paths) == 0
	if cleanPreview {
		paths = append([]string(nil), result.Applied...)
		if len(paths) == 0 {
			paths = append(paths, result.Paths...)
		}
		if len(paths) == 0 {
			paths = append(paths, result.CleanPaths...)
		}
	}
	return map[string]any{
		"paths":           paths,
		"overlap_job_ids": result.OverlapJobIDs,
		"overlay_id":      strings.TrimSpace(result.JobID),
		"clean_preview":   cleanPreview,
	}
}

// RenderPromotePartialPathBanner handles previews with clean and conflict paths.
func RenderPromotePartialPathBanner(ctx context.Context, mergeOutput string) (string, error) {
	result, ok := parseWorkerMergeResultOutput(mergeOutput)
	if !ok {
		return "", nil
	}
	cleanPaths := append([]string(nil), result.CleanPaths...)
	if len(cleanPaths) == 0 {
		return "", nil
	}
	conflictPaths := conflictPathsFromResult(result)
	if len(conflictPaths) == 0 {
		return "", nil
	}
	block, err := RenderGuidance(ctx, "enrich/promote-partial-path", map[string]any{
		"overlay_id":     strings.TrimSpace(result.JobID),
		"clean_paths":    cleanPaths,
		"conflict_paths": conflictPaths,
		"spill_path":     strings.TrimSpace(result.SpillPath),
	})
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" || !strings.Contains(block, "BANNER_PROMOTE_PARTIAL_PATH") {
		return "", fmt.Errorf("promote-partial-path banner missing code")
	}
	return "\n" + block, nil
}

// RenderPromoteConflictDigestBanner appends inline hunk ranges when conflict_digest is present.
func RenderPromoteConflictDigestBanner(ctx context.Context, mergeOutput string) (string, error) {
	result, ok := parseWorkerMergeResultOutput(mergeOutput)
	if !ok || len(result.ConflictDigest) == 0 {
		return "", nil
	}
	block, err := RenderGuidance(ctx, "enrich/promote-conflict-digest", map[string]any{
		"overlay_id":            strings.TrimSpace(result.JobID),
		"conflict_digest":       conflictDigestRows(result),
		"spill_path":            strings.TrimSpace(result.SpillPath),
		"overlay_intent":        overlayIntentLine(result),
		"scoped_hunk_threshold": tooloutput.PromoteScopedPathInlineHunkThreshold,
	})
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" || !strings.Contains(block, "BANNER_PROMOTE_CONFLICT_DIGEST") {
		return "", fmt.Errorf("promote-conflict-digest banner missing code")
	}
	return "\n" + block, nil
}

// RenderPromoteHighConflictBanner handles previews above the conflict threshold.
func RenderPromoteHighConflictBanner(ctx context.Context, mergeOutput string) (string, error) {
	result, ok := parseWorkerMergeResultOutput(mergeOutput)
	if !ok {
		return "", nil
	}
	maxHunks, paths := conflictHunkPaths(result, "", tooloutput.PromoteHighConflictHunkThreshold+1, 0)
	if maxHunks <= tooloutput.PromoteHighConflictHunkThreshold {
		return "", nil
	}
	block, err := RenderGuidance(ctx, "enrich/promote-high-conflict", map[string]any{
		"overlay_id":     strings.TrimSpace(result.JobID),
		"hunk_count":     maxHunks,
		"conflict_paths": paths,
		"spill_path":     strings.TrimSpace(result.SpillPath),
		"threshold":      tooloutput.PromoteHighConflictHunkThreshold,
	})
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" || !strings.Contains(block, "BANNER_PROMOTE_HIGH_CONFLICT") {
		return "", fmt.Errorf("promote-high-conflict banner missing code")
	}
	return "\n" + block, nil
}

// RenderPromoteHunkPickBanner suggests per-hunk cherry-pick for overlapping_edit conflicts.
func RenderPromoteHunkPickBanner(ctx context.Context, mergeOutput string) (string, error) {
	result, ok := parseWorkerMergeResultOutput(mergeOutput)
	if !ok {
		return "", nil
	}
	maxHunks, paths := conflictHunkPaths(result, api.WorkerPromoteConflictTierOverlappingEdit, 1, tooloutput.PromoteHighConflictHunkThreshold)
	if maxHunks <= 0 || maxHunks > tooloutput.PromoteHighConflictHunkThreshold {
		return "", nil
	}
	block, err := RenderGuidance(ctx, "enrich/promote-hunk-pick", map[string]any{
		"overlay_id":     strings.TrimSpace(result.JobID),
		"hunk_count":     maxHunks,
		"conflict_paths": paths,
		"threshold":      tooloutput.PromoteHighConflictHunkThreshold,
	})
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" || !strings.Contains(block, "BANNER_PROMOTE_HUNK_PICK") {
		return "", fmt.Errorf("promote-hunk-pick banner missing code")
	}
	return "\n" + block, nil
}

// RenderPromoteOverlayBodyBanner reminds coordinators that unpromoted bodies
// live in spill/scoped preview — not on primary — and reject discards work.
func RenderPromoteOverlayBodyBanner(ctx context.Context, mergeOutput string) (string, error) {
	result, ok := parseWorkerMergeResultOutput(mergeOutput)
	if !ok {
		return "", nil
	}
	conflictPaths := conflictPathsFromResult(result)
	if len(conflictPaths) == 0 {
		return "", nil
	}
	block, err := RenderGuidance(ctx, "enrich/promote-overlay-body", map[string]any{
		"overlay_id":      strings.TrimSpace(result.JobID),
		"conflict_paths":  conflictPaths,
		"spill_path":      strings.TrimSpace(result.SpillPath),
		"overlap_job_ids": result.OverlapJobIDs,
	})
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" || !strings.Contains(block, "BANNER_PROMOTE_OVERLAY_BODY") {
		return "", fmt.Errorf("promote-overlay-body banner missing code")
	}
	return "\n" + block, nil
}

// RenderPromoteSpillFirstBanner handles line-shift reconciliation.
func RenderPromoteSpillFirstBanner(ctx context.Context, mergeOutput string) (string, error) {
	result, ok := parseWorkerMergeResultOutput(mergeOutput)
	if !ok {
		return "", nil
	}
	if !pathStatusHasConflictTier(result, api.WorkerPromoteConflictTierLineShift) {
		return "", nil
	}
	spill := strings.TrimSpace(result.SpillPath)
	maxHunks, _ := conflictHunkPaths(result, api.WorkerPromoteConflictTierLineShift, 1, 0)
	if spill == "" && maxHunks <= tooloutput.PromoteHighConflictHunkThreshold {
		return "", nil
	}
	block, err := RenderGuidance(ctx, "enrich/promote-spill-first", map[string]any{
		"overlay_id": strings.TrimSpace(result.JobID),
		"spill_path": spill,
	})
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" || !strings.Contains(block, "BANNER_PROMOTE_SPILL_FIRST") {
		return "", fmt.Errorf("promote-spill-first banner missing code")
	}
	return "\n" + block, nil
}

// RenderPromoteScopedHunksBanner notes inline hunk bodies from scoped preview_overlay(path=).
func RenderPromoteScopedHunksBanner(ctx context.Context, mergeOutput string) (string, error) {
	result, ok := parseWorkerMergeResultOutput(mergeOutput)
	if !ok || !tooloutput.ScopedPathInlineHunks(result) {
		return "", nil
	}
	path := strings.TrimSpace(result.Paths[0])
	hunkCount := len(result.Conflicts[0].Hunks)
	if hunkCount <= 0 {
		hunkCount = pathHunkCount(result, path)
	}
	block, err := RenderGuidance(ctx, "enrich/promote-scoped-hunks", map[string]any{
		"overlay_id":       strings.TrimSpace(result.JobID),
		"path":             path,
		"hunk_count":       hunkCount,
		"threshold":        tooloutput.PromoteHighConflictHunkThreshold,
		"inline_threshold": tooloutput.PromoteScopedPathInlineHunkThreshold,
		"spill_path":       strings.TrimSpace(result.SpillPath),
	})
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" || !strings.Contains(block, "BANNER_PROMOTE_SCOPED_HUNKS") {
		return "", fmt.Errorf("promote-scoped-hunks banner missing code")
	}
	return "\n" + block, nil
}

// RenderOverlayMergePlanBanner appends session-level overlay promote sequencing from pack_board.
func RenderOverlayMergePlanBanner(ctx context.Context, packBoardOutput string) (string, error) {
	packBoardOutput = strings.TrimSpace(packBoardOutput)
	if packBoardOutput == "" || packBoardOutput[0] != '{' {
		return "", nil
	}
	var view api.BoardView
	if err := json.Unmarshal([]byte(packBoardOutput), &view); err != nil {
		return "", nil //nolint:nilerr // unparseable board output means no banner, not a render failure
	}
	if view.OverlayMergePlan == nil || view.OverlayMergePlan.PendingCount == 0 {
		return "", nil
	}
	plan := view.OverlayMergePlan
	shared := make([]map[string]any, 0, len(plan.SharedPaths))
	for _, row := range plan.SharedPaths {
		shared = append(shared, map[string]any{
			"path":    row.Path,
			"job_ids": row.WorkerIDs,
		})
	}
	block, err := RenderGuidance(ctx, "enrich/overlay-merge-plan", map[string]any{
		"pending_count":    plan.PendingCount,
		"promote_sequence": plan.PromoteSequence,
		"shared_paths":     shared,
	})
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" || !strings.Contains(block, "BANNER_OVERLAY_MERGE_PLAN") {
		return "", fmt.Errorf("overlay-merge-plan banner missing code")
	}
	return "\n" + block, nil
}

func conflictHunkPaths(result api.WorkerMergeResult, tier api.WorkerPromoteConflictTier, minHunks, maxAllowed int) (int, []string) {
	maxHunks := 0
	var paths []string
	for _, row := range result.PathStatus {
		if row.Status != api.WorkerPromotePathOutcomeConflict || row.HunkCount < minHunks || (maxAllowed > 0 && row.HunkCount > maxAllowed) || (tier != "" && row.ConflictTier != tier) {
			continue
		}
		if row.HunkCount > maxHunks {
			maxHunks = row.HunkCount
		}
		paths = append(paths, row.Path)
	}
	return maxHunks, paths
}

// RenderPromoteOrderBanner handles order-sensitive promotions.
func RenderPromoteOrderBanner(ctx context.Context, mergeOutput string) (string, error) {
	result, ok := parseWorkerMergeResultOutput(mergeOutput)
	if !ok || result.PromoteOrder == "" || result.PromoteOrder == api.WorkerPromoteOrderIndependent {
		return "", nil
	}
	block, err := RenderGuidance(ctx, "enrich/promote-order", map[string]any{
		"overlay_id":         strings.TrimSpace(result.JobID),
		"promote_order":      string(result.PromoteOrder),
		"promote_after":      result.PromoteAfter,
		"blocked_by":         result.BlockedBy,
		"promote_order_note": promoteOrderNoteFromResult(result),
	})
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" || !strings.Contains(block, "BANNER_PROMOTE_ORDER") {
		return "", fmt.Errorf("promote-order banner missing code")
	}
	return "\n" + block, nil
}

func promoteOrderNoteFromResult(result api.WorkerMergeResult) string {
	for _, row := range result.PathStatus {
		if note := strings.TrimSpace(row.PromoteOrderNote); note != "" {
			return note
		}
	}
	return ""
}

func conflictDigestRows(result api.WorkerMergeResult) []map[string]any {
	out := make([]map[string]any, 0, len(result.ConflictDigest))
	for _, row := range result.ConflictDigest {
		tier := string(row.ConflictTier)
		if tier == "" {
			tier = pathConflictTier(result, row.Path)
		}
		out = append(out, map[string]any{
			"path":               row.Path,
			"summary":            row.Summary,
			"hunk_count":         pathHunkCount(result, row.Path),
			"conflict_tier":      tier,
			"branch_delta_lines": branchDeltaLines(row.BranchDelta),
		})
	}
	return out
}

func overlayIntentLine(result api.WorkerMergeResult) string {
	if result.OverlayIntent == nil {
		return ""
	}
	return strings.TrimSpace(result.OverlayIntent.Summary)
}

func branchDeltaLines(delta string) []string {
	delta = strings.TrimSpace(delta)
	if delta == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(delta, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
		if len(out) >= tooloutput.MaxInlineConflictSummaryLines {
			out = append(out, "…")
			break
		}
	}
	return out
}

func pathConflictTier(result api.WorkerMergeResult, path string) string {
	path = strings.TrimSpace(path)
	for _, row := range result.PathStatus {
		if row.Path == path && row.Status == api.WorkerPromotePathOutcomeConflict {
			return string(row.ConflictTier)
		}
	}
	return ""
}

func pathStatusHasConflictTier(result api.WorkerMergeResult, tier api.WorkerPromoteConflictTier) bool {
	for _, row := range result.PathStatus {
		if row.Status == api.WorkerPromotePathOutcomeConflict && row.ConflictTier == tier {
			return true
		}
	}
	for _, row := range result.ConflictDigest {
		if row.ConflictTier == tier {
			return true
		}
	}
	return false
}

func conflictPathsFromResult(result api.WorkerMergeResult) []string {
	seen := map[string]struct{}{}
	var out []string
	add := func(path string) {
		path = strings.TrimSpace(path)
		if path == "" {
			return
		}
		if _, ok := seen[path]; ok {
			return
		}
		seen[path] = struct{}{}
		out = append(out, path)
	}
	for _, row := range result.PathStatus {
		if row.Status == api.WorkerPromotePathOutcomeConflict {
			add(row.Path)
		}
	}
	for _, c := range result.Conflicts {
		add(c.Path)
	}
	for _, row := range result.ConflictDigest {
		add(row.Path)
	}
	return out
}

func pathHunkCount(result api.WorkerMergeResult, path string) int {
	path = strings.TrimSpace(path)
	for _, row := range result.PathStatus {
		if row.Path == path && row.Status == api.WorkerPromotePathOutcomeConflict {
			return row.HunkCount
		}
	}
	return 0
}

func parseWorkerMergeResultOutput(mergeOutput string) (api.WorkerMergeResult, bool) {
	mergeOutput = strings.TrimSpace(mergeOutput)
	if mergeOutput == "" || strings.HasPrefix(mergeOutput, hostmarker.Rejected) {
		return api.WorkerMergeResult{}, false
	}
	jsonPart := mergeOutput
	if strings.HasPrefix(mergeOutput, "{") {
		if idx := strings.Index(mergeOutput, "\n"+hostmarker.Rejected); idx > 0 {
			jsonPart = mergeOutput[:idx]
		}
	} else {
		return api.WorkerMergeResult{}, false
	}
	var result api.WorkerMergeResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(jsonPart)), &result); err != nil {
		return api.WorkerMergeResult{}, false
	}
	return result, true
}

// RenderOverlayRebaseConflictBanners renders one OVERLAY_REBASE_CONFLICT block
// for each stacked child whose rebase conflicted after its parent overlay
// promoted.
func RenderOverlayRebaseConflictBanners(ctx context.Context, parentOverlayID string, rebased []api.OverlayRebaseOutcome) (string, error) {
	if len(rebased) == 0 {
		return "", nil
	}
	var out strings.Builder
	for _, child := range rebased {
		if child.Status != api.WorkerMergeStatusRebasing || len(child.Conflicts) == 0 {
			continue
		}
		block, err := RenderGuidance(ctx, "enrich/overlay-rebase-conflict", map[string]any{
			"overlay_id":      child.OverlayID,
			"base_overlay_id": parentOverlayID,
			"conflict_count":  len(child.Conflicts),
			"conflict_paths":  child.Conflicts,
		})
		if err != nil {
			return "", err
		}
		block = strings.TrimSpace(block)
		if block == "" || !strings.Contains(block, "OVERLAY_REBASE_CONFLICT") {
			return "", fmt.Errorf("overlay-rebase-conflict banner missing code for child %s", child.OverlayID)
		}
		out.WriteString("\n")
		out.WriteString(block)
	}
	return out.String(), nil
}

// RenderOverlayParentRejectedBanners renders one OVERLAY_PARENT_REJECTED block
// for each stacked child a reject_overlay orphaned.
func RenderOverlayParentRejectedBanners(ctx context.Context, parentOverlayID string, orphanedChildIDs []string) (string, error) {
	if len(orphanedChildIDs) == 0 {
		return "", nil
	}
	var out strings.Builder
	for _, childID := range orphanedChildIDs {
		block, err := RenderGuidance(ctx, "enrich/overlay-parent-rejected", map[string]any{
			"overlay_id":      strings.TrimSpace(childID),
			"base_overlay_id": parentOverlayID,
		})
		if err != nil {
			return "", err
		}
		block = strings.TrimSpace(block)
		if block == "" || !strings.Contains(block, "OVERLAY_PARENT_REJECTED") {
			return "", fmt.Errorf("overlay-parent-rejected banner missing code for child %s", childID)
		}
		out.WriteString("\n")
		out.WriteString(block)
	}
	return out.String(), nil
}

// RenderWorkerInFlightRoster renders enrich/worker-inflight-roster.md for batch dispatch.
func RenderWorkerInFlightRoster(ctx context.Context, workers []WorkerRosterLine) (string, error) {
	if len(workers) == 0 {
		return "", nil
	}
	rows := make([]map[string]any, len(workers))
	for i, worker := range workers {
		row := map[string]any{
			"job_prefix":    worker.JobPrefix,
			"agent":         worker.Agent,
			"status":        worker.Status,
			"scope_summary": worker.ScopeSummary,
		}
		if touch := strings.TrimSpace(worker.TouchSummary); touch != "" {
			row["touch_summary"] = touch
		}
		rows[i] = row
	}
	block, err := RenderGuidance(ctx, "enrich/worker-inflight-roster", map[string]any{
		"workers": rows,
	})
	if err != nil {
		return "", err
	}
	block = strings.TrimSpace(block)
	if block == "" || !strings.Contains(block, "BANNER_WORKER_INFLIGHT_ROSTER") {
		return "", fmt.Errorf("worker-inflight-roster banner missing code")
	}
	return "\n" + block, nil
}

// BuildWorkerRosterLines maps worker jobs into roster rows.
func BuildWorkerRosterLines(tasks []api.WorkerTask) []WorkerRosterLine {
	out := make([]WorkerRosterLine, 0, len(tasks))
	for _, task := range tasks {
		agent := strings.TrimSpace(task.AgentType)
		if agent == "" {
			agent = "worker"
		}
		line := WorkerRosterLine{
			JobPrefix:    RosterJobIDPrefix(task.ID),
			Agent:        agent,
			Status:       string(task.Status),
			ScopeSummary: task.EffectiveScope().Summary(),
		}
		if len(task.TouchedPaths) > 0 {
			line.TouchSummary = formatRosterTouchSummary(task.TouchedPaths)
		}
		out = append(out, line)
	}
	return out
}

func formatRosterTouchSummary(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	if len(paths) == 1 {
		return "touched · " + paths[0]
	}
	return "touched · " + paths[0] + " (+ " + strconv.Itoa(len(paths)-1) + " more)"
}

// RosterJobIDPrefix returns a short job id prefix for roster lines (e.g. 8a3f…).
func RosterJobIDPrefix(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "????"
	}
	return runeclamp.ClampBytes(id, shortIDBytes)
}

// RenderSynthesizedSurveyPreamble renders enrich/worker-survey-preamble.md for host-compiled surveys.
func RenderSynthesizedSurveyPreamble(ctx context.Context, agentType string) (string, error) {
	block, err := RenderGuidance(ctx, "enrich/worker-survey-preamble", map[string]any{
		"agent_type": strings.TrimSpace(agentType),
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(block), nil
}
