package session

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/session/lifecycle"
)

func (m *Manager) sessionRootID(ctx context.Context, sessionID string) string {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || m == nil || m.store == nil {
		return sessionID
	}
	if root := strings.TrimSpace(RootSessionID(ctx, m.store, sessionID)); root != "" {
		return root
	}
	return sessionID
}

func appendStopError(errs []error, operation, sessionID string, err error) []error {
	if err == nil {
		return errs
	}
	return append(errs, fmt.Errorf("%s for session %s: %w", operation, sessionID, err))
}

func (m *Manager) captureSessionTurn(ctx context.Context, sessionID string) (lifecycle.Turn, error) {
	return m.stopState.Capture(m.sessionRootID(ctx, sessionID))
}

func (m *Manager) sessionStopInProgress(ctx context.Context, sessionID string) bool {
	if m == nil {
		return false
	}
	return m.stopState.InProgress(m.sessionRootID(ctx, sessionID))
}

func (m *Manager) WithSessionTreeAdmission(ctx context.Context, sessionID string, fn func() error) error {
	return m.stopState.WithAdmission(m.sessionRootID(ctx, sessionID), fn)
}
