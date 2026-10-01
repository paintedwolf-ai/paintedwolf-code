package session

import (
	"context"
	"errors"
	"strings"
)

// RecoverSession repairs one session in the live process. Repeated calls are idempotent.
func (m *Manager) RecoverSession(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if m == nil || m.store == nil || sessionID == "" {
		return nil
	}
	turnErr := m.RecoverOrphanedTurnsForSession(ctx, sessionID)
	rewindErr := m.RecoverRewindsForSession(ctx, sessionID)
	return errors.Join(turnErr, rewindErr)
}
