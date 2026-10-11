package execution

import (
	"context"
	"log/slog"

	"github.com/lycaon/lycaon/pkg/api"
)

// ScratchDir prepares the session's own scratch folder for one
// iteration's tools. A failure leaves the context without scratch, so
// @scratch refuses with SESSION_SCRATCH_UNAVAILABLE; the cause is logged here.
func (m *Lifetime) ScratchDir(ctx context.Context, sess *api.Session) string {
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

// ReclaimScratch clears every idle session's scratch. A session with a turn in
// flight or a live process keeps its folder: both can still be writing to it.
func (m *Lifetime) ReclaimScratch(ctx context.Context) error {
	if m == nil || m.scratch == nil {
		return nil
	}
	_, err := m.scratch.Reclaim(ctx, m.holdIdleScratch)
	return err
}

// holdIdleScratch claims a session's turn lock so no turn starts while its
// scratch is removed, and passes over a session with a live process.
func (m *Lifetime) holdIdleScratch(sessionID string) (func(), bool) {
	unlock, ok := m.TryIdleMutation(sessionID)
	if !ok {
		return nil, false
	}
	if m.processes != nil && m.processes.HasRunning(sessionID) {
		unlock()
		return nil, false
	}
	return unlock, true
}
