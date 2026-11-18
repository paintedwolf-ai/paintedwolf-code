package store

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/timelayout"
)

// UserIntentBefore reads the latest human opener at a worker's creation boundary.
func (t *sqlTurnTelemetry) UserIntentBefore(ctx context.Context, sessionID string, before time.Time) (time.Time, error) {
	value, err := t.core.queries.UserIntentBefore(ctx, db.UserIntentBeforeParams{SessionID: sessionID, Ts: timelayout.Format(before)})
	if db.IsNoRows(err) {
		return before, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return db.ParseTime(value)
}

func cloneTurnClock(clock TurnClock) TurnClock {
	if clock.RunningAt != nil {
		since := *clock.RunningAt
		clock.RunningAt = &since
	}
	if clock.SettledAt != nil {
		settled := *clock.SettledAt
		clock.SettledAt = &settled
	}
	return clock
}
