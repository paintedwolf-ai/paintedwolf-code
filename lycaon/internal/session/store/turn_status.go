package store

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
)

// LatestTurnStatus reads the session turn head, including a resumed execution.
func (t *sqlTurns) LatestTurnStatus(ctx context.Context, sessionID string) (TurnStatus, error) {
	status, err := t.core.queries.GetLatestTurnStatus(ctx, strings.TrimSpace(sessionID))
	if db.IsNoRows(err) {
		return "", nil
	}
	return TurnStatus(status), err
}
