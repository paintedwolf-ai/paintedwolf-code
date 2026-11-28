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
	Reader           prompts.Reader
	ProgressSnapshot string
	ProgressMissing  bool
	VerifyRequired   bool
	// Workers is the bounded roster, one map per worker, and WorkersOmitted
	// how many workers it left out.
	Workers        []map[string]any
	WorkersOmitted int
}

// RenderTurnStateBlock renders dynamic turn-state guidance.
func RenderTurnStateBlock(ctx context.Context, renderer *prompts.InjectRenderer, sessionID string, in TurnStateInjectInput) (string, error) {
	if renderer == nil {
		return "", fmt.Errorf("inject renderer not configured")
	}
	reader := in.Reader
	if !reader.Valid() {
		return "", fmt.Errorf("turn-state inject requires a valid reader")
	}
	occ := map[string]any{
		"progress_snapshot": in.ProgressSnapshot,
		"progress_missing":  in.ProgressMissing,
		"verify_required":   in.VerifyRequired,
		"workers":           in.Workers,
		"workers_omitted":   in.WorkersOmitted,
	}
	// Each state present on the roster gates the guidance for it.
	for _, w := range in.Workers {
		if state, _ := w["state"].(string); state != "" {
			occ["workers_"+state] = true
		}
	}
	block, err := anchor.RenderInformWithReader(ctx, anchor.InjectTurnState, anchor.MatchContext{
		Surface:   "coordinator",
		SessionID: sessionID,
	}, renderer, reader, occ)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(block), nil
}
