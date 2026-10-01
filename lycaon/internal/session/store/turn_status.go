package store

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/db"
)

// LatestTurnStatus reads the session turn head, including a resumed execution.
func (s *SQL) LatestTurnStatus(ctx context.Context, sessionID string) (TurnStatus, error) {
	status, err := s.queries.GetLatestTurnStatus(ctx, strings.TrimSpace(sessionID))
	if db.IsNoRows(err) {
		return "", nil
	}
	return TurnStatus(status), err
}

func (s *Memory) LatestTurnStatus(ctx context.Context, sessionID string) (TurnStatus, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.turns[s.turnHeads[strings.TrimSpace(sessionID)]].Status, nil
}
