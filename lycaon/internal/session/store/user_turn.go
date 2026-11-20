package store

import (
	"context"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/timelayout"
)

// UserIntentBefore reads the latest human opener at a worker's creation boundary.
func (t *sqlTranscript) UserIntentBefore(ctx context.Context, sessionID string, before time.Time) (time.Time, error) {
	value, err := t.core.queries.UserIntentBefore(ctx, db.UserIntentBeforeParams{SessionID: sessionID, Ts: timelayout.Format(before)})
	if db.IsNoRows(err) {
		return before, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return db.ParseTime(value)
}
