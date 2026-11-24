package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
)

// CompletionCueState persists per-intent cue delivery bits for a session.
type CompletionCueState struct {
	IntentID           string
	OutcomeDelivered   bool
	FinalItemDelivered bool
}

// CompletionCueRepository persists coordinator completion-cue delivery state.
type CompletionCueRepository interface {
	GetCompletionCueState(ctx context.Context, sessionID string) (CompletionCueState, error)
	SaveCompletionCueState(ctx context.Context, sessionID string, state CompletionCueState) error
}

type sqlCompletionCues struct{ core *sqlCore }

func (c *sqlCompletionCues) GetCompletionCueState(ctx context.Context, sessionID string) (CompletionCueState, error) {
	if err := ctx.Err(); err != nil {
		return CompletionCueState{}, err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return CompletionCueState{}, nil
	}
	row, err := c.core.queries.GetCoordinatorCompletionCue(ctx, sessionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CompletionCueState{}, nil
		}
		return CompletionCueState{}, err
	}
	return CompletionCueState{
		IntentID:           row.IntentID,
		OutcomeDelivered:   row.OutcomeDelivered != 0,
		FinalItemDelivered: row.FinalItemDelivered != 0,
	}, nil
}

func (c *sqlCompletionCues) SaveCompletionCueState(ctx context.Context, sessionID string, state CompletionCueState) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	var outcome, finalItem int64
	if state.OutcomeDelivered {
		outcome = 1
	}
	if state.FinalItemDelivered {
		finalItem = 1
	}
	return c.core.queries.UpsertCoordinatorCompletionCue(ctx, db.UpsertCoordinatorCompletionCueParams{
		SessionID:          sessionID,
		IntentID:           strings.TrimSpace(state.IntentID),
		OutcomeDelivered:   outcome,
		FinalItemDelivered: finalItem,
	})
}
