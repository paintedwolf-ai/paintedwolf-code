package cost

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/lycaon/lycaon/internal/db"
)

// ConversationCall is the route and start of a session's latest
// conversation model call.
type ConversationCall struct {
	ProviderID string
	Model      string
	StartedAt  time.Time
}

// LastConversationCall returns the session's latest coordinator or worker
// model call. Utility calls, such as summaries, run on their own routes and
// never touch the conversation's cached prefix, so they are not counted.
func (t *SQLTracker) LastConversationCall(ctx context.Context, sessionID string) (ConversationCall, bool, error) {
	if t == nil || t.db == nil {
		return ConversationCall{}, false, fmt.Errorf("cost tracker database is not configured")
	}
	var call ConversationCall
	var started string
	err := t.db.QueryRowContext(ledgerCtx(ctx), `
		SELECT provider_id, model, started_at FROM llm_calls
		WHERE session_id = ? AND caller IN (?, ?)
		ORDER BY started_at DESC LIMIT 1
	`, strings.TrimSpace(sessionID), CallerCoordinator, CallerWorker).Scan(&call.ProviderID, &call.Model, &started)
	if errors.Is(err, sql.ErrNoRows) {
		return ConversationCall{}, false, nil
	}
	if err != nil {
		return ConversationCall{}, false, err
	}
	call.StartedAt, err = db.ParseTime(started)
	if err != nil {
		return ConversationCall{}, false, err
	}
	return call, true, nil
}
