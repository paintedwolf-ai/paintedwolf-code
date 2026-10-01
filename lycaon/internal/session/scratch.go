package session

import (
	"context"
	"log/slog"

	"github.com/lycaon/lycaon/pkg/api"
)

// sessionScratchDir prepares the session's own scratch folder for one
// iteration's tools. A failure leaves the context without scratch, so
// @scratch refuses with SESSION_SCRATCH_UNAVAILABLE; the cause is logged here.
func (m *Manager) sessionScratchDir(ctx context.Context, sess *api.Session) string {
	if m == nil || m.scratch == nil || sess == nil {
		return ""
	}
	dir, err := m.scratch.Ensure(sess.ID)
	if err != nil {
		slog.ErrorContext(ctx, "prepare session scratch", "session_id", sess.ID, "error", err)
		return ""
	}
	return dir
}

// removeScratch deletes the scratch folders of a deleted session tree.
func (m *Manager) removeScratch(ctx context.Context, sessionIDs []string) {
	if m == nil || m.scratch == nil || len(sessionIDs) == 0 {
		return
	}
	if err := m.scratch.Remove(sessionIDs...); err != nil {
		slog.WarnContext(ctx, "remove deleted session scratch", "session_ids", sessionIDs, "error", err)
	}
}

// ReclaimScratch clears every idle session's scratch. A session with a turn in
// flight or a live process keeps its folder: both can still be writing to it.
func (m *Manager) ReclaimScratch(ctx context.Context) error {
	if m == nil || m.scratch == nil {
		return nil
	}
	_, err := m.scratch.Reclaim(ctx, m.holdIdleScratch)
	return err
}

// holdIdleScratch claims a session's turn lock so no turn starts while its
// scratch is removed, and passes over a session with a live process.
func (m *Manager) holdIdleScratch(sessionID string) (func(), bool) {
	unlock, ok := m.TryIdleMutation(sessionID)
	if !ok {
		return nil, false
	}
	if m.bgRegistry.HasRunning(sessionID) {
		unlock()
		return nil, false
	}
	return unlock, true
}
