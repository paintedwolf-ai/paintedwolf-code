package execution

import (
	"context"
	"github.com/lycaon/lycaon/internal/scratch"
	"log/slog"

	"github.com/lycaon/lycaon/pkg/api"
)

// SessionDirectories prepares the writable scratch and host-owned output
// capabilities before the iteration can launch a command.
func (m *Lifetime) SessionDirectories(ctx context.Context, sess *api.Session) scratch.Directories {
	if m == nil || m.scratch == nil || sess == nil {
		return scratch.Directories{}
	}
	dirs, err := m.scratch.Ensure(sess.ID)
	if err != nil {
		slog.ErrorContext(ctx, "prepare session folders", "session_id", sess.ID, "error", err)
		return scratch.Directories{}
	}
	return dirs
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
