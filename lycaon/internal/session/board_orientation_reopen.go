package session

import (
	"context"
)

// ReopenBoardOrientationOnRootAttach refreshes open project sessions.
func (m *Manager) ReopenBoardOrientationOnRootAttach(ctx context.Context, projectID string) {
	if m == nil || m.store == nil || projectID == "" {
		return
	}
	sessions, err := m.store.List(ctx)
	if err != nil {
		return
	}
	rt := m.ensureCoordinatorRuntime()
	board := rt.Board()
	if board == nil {
		return
	}
	for _, sess := range sessions {
		if sess == nil || sess.ProjectID != projectID {
			continue
		}
		board.InvalidateOrientation(sess.ID)
	}
}
