package submissions

import (
	"context"
	"log/slog"
)

// DrainQueue bypasses round-idle gating for a ready draft.
func (m *Service) DrainQueue(ctx context.Context, id string) {
	if m == nil || m.store == nil {
		return
	}
	ctx, unlockDispatch := m.LockDispatch(ctx, id)
	defer unlockDispatch()
	if _, err := m.dispatchPromptSubmissions(ctx, id, ""); err != nil {
		slog.ErrorContext(ctx, "drain prompt submissions", "session_id", id, "error", err)
	}
}
