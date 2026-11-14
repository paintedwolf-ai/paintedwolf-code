package cost

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// SessionUsage sums one session's model calls over a window.
type SessionUsage struct {
	Calls        int
	Tokens       int
	SpendNanoUSD int64
	// Priced is false when any call in the window has no estimate.
	Priced bool
}

// SessionUsageSince sums the calls a session started at or after since.
func (t *SQLTracker) SessionUsageSince(ctx context.Context, sessionID string, since time.Time) (SessionUsage, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return SessionUsage{}, fmt.Errorf("session usage requires a session id")
	}
	var out SessionUsage
	var unpriced int
	err := t.db.QueryRowContext(ctx, `
		SELECT COUNT(*),
		       COALESCE(SUM(prompt_tokens + completion_tokens), 0),
		       COALESCE(SUM(estimated_nano_usd), 0),
		       COALESCE(SUM(estimated_nano_usd IS NULL), 0)
		FROM llm_calls
		WHERE session_id = ? AND started_at >= ?`,
		sessionID, db.FormatTime(since.UTC()),
	).Scan(&out.Calls, &out.Tokens, &out.SpendNanoUSD, &unpriced)
	if err != nil {
		return SessionUsage{}, fmt.Errorf("session usage: %w", err)
	}
	out.Priced = unpriced == 0
	return out, nil
}
