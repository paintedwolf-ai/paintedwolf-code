package inject

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/internal/sandbox"
)

// TurnStateInjectInput carries variables for inject.turn_state.
type TurnStateInjectInput struct {
	Reader             prompts.Reader
	ProgressSnapshot   string
	ProgressMissing    bool
	VerifyRequired     bool
	PendingOverlayJobs []string
	PartialWorkerJobs  []string
}

// RenderTurnStateBlock renders dynamic turn-state guidance.
func RenderTurnStateBlock(ctx context.Context, renderer *prompts.InjectRenderer, sessionID string, in TurnStateInjectInput) (string, error) {
	if renderer == nil {
		return "", fmt.Errorf("inject renderer not configured")
	}
	reader := in.Reader
	if !reader.Valid() {
		profiles, err := sandbox.LoadToolProfiles()
		if err != nil {
			return "", err
		}
		reader, err = prompts.NewReader(prompts.ReaderInput{
			ProfileID: prompts.CoordinatorProfileID,
			Profiles:  profiles,
			RootCount: 1,
			WebSearch: true,
		})
		if err != nil {
			return "", err
		}
	}
	occ := map[string]any{
		"progress_snapshot":    in.ProgressSnapshot,
		"progress_missing":     in.ProgressMissing,
		"verify_required":      in.VerifyRequired,
		"pending_overlay_jobs": in.PendingOverlayJobs,
		"partial_worker_jobs":  in.PartialWorkerJobs,
	}
	vars, err := prompts.Vars(reader, occ)
	if err != nil {
		return "", err
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
