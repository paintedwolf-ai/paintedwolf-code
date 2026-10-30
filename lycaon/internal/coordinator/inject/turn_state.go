package inject

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
)

// TurnStateInjectInput carries variables for inject.turn_state.
type TurnStateInjectInput struct {
	ProgressSnapshot         string
	ProgressMissing          bool
	ProfileHasUpdateProgress bool
	VerifyRequired           bool
	PendingOverlayJobs       []string
	PartialWorkerJobs        []string
}

// RenderTurnStateBlock renders dynamic turn-state guidance.
func RenderTurnStateBlock(ctx context.Context, renderer *prompts.InjectRenderer, sessionID string, in TurnStateInjectInput) (string, error) {
	if renderer == nil {
		return "", fmt.Errorf("inject renderer not configured")
	}
	vars := map[string]any{
		"progress_snapshot":           in.ProgressSnapshot,
		"progress_missing":            in.ProgressMissing,
		"profile_has_update_progress": in.ProfileHasUpdateProgress,
		"verify_required":             in.VerifyRequired,
		"pending_overlay_jobs":        in.PendingOverlayJobs,
		"partial_worker_jobs":         in.PartialWorkerJobs,
	}
	block, err := anchor.RenderInform(ctx, anchor.InjectTurnState, anchor.MatchContext{
		Surface:   "coordinator",
		SessionID: sessionID,
	}, renderer, vars)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(block), nil
}
