package execution

import (
	"context"
	"log/slog"

	"github.com/lycaon/lycaon/internal/session/store"
)

func (m *Journal) HostTurnBlocked(ctx context.Context, sessionID string) bool {
	status, err := m.store.LatestTurnStatus(ctx, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "read turn status for automatic continuation", "session_id", sessionID, "error", err)
		return true
	}
	return status == store.TurnStatusFailed || status == store.TurnStatusInterrupted
}
