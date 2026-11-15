package guidance

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

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
