package chats

import (
	"context"
)

// ReopenBoardOrientationOnRootAttach refreshes open project sessions.
func (m *Service) ReopenOrientation(ctx context.Context, projectID string, board Orientation) {
	if m == nil || m.store == nil || projectID == "" {
		return
	}
	sessions, err := m.store.List(ctx)
	if err != nil {
		return
	}
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

type Orientation interface{ InvalidateOrientation(string) }
