package submissions

import (
	"context"
	"errors"
	"log/slog"

	"github.com/lycaon/lycaon/internal/session/lifecycle"
)

// DrainQueue bypasses round-idle gating for a ready draft.
func (m *Service) DrainQueue(ctx context.Context, id string) {
	if m == nil || m.store == nil {
		return
	}
	ctx, unlockDispatch := m.LockDispatch(ctx, id)
	defer unlockDispatch()
	if _, err := m.dispatchPromptSubmissions(ctx, id, ""); err != nil && !errors.Is(err, lifecycle.ErrStopping) {
		slog.ErrorContext(ctx, "drain prompt submissions", "session_id", id, "error", err)
	}
}
