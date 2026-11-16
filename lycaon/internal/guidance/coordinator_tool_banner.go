package guidance

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/lycaon/lycaon/internal/runeclamp"
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
