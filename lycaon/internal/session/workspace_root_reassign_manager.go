package session

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReassignSessionsAfterRootDetach retargets open sessions when a folder root is removed.
func (m *Manager) ReassignSessionsAfterRootDetach(ctx context.Context, projectID, detachedRootID string, after []projectroot.RootRef) {
	if m == nil || m.store == nil {
		return
	}
	projectID = strings.TrimSpace(projectID)
	detachedRootID = strings.TrimSpace(detachedRootID)
	if projectID == "" || detachedRootID == "" {
		return
	}
	replacement := ""
	if r, err := projectroot.PrimaryRoot(after); err == nil {
		replacement = r.ID
	}
	_ = m.store.ReassignSessionsWorkspaceRoot(ctx, projectID, detachedRootID, replacement)
}

// InvalidateSessionWorkspacePaths clears hydrated workspace paths.
func (m *Manager) InvalidateSessionWorkspacePaths(ctx context.Context, projectID string) {
	if m == nil || m.store == nil {
		return
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return
	}
	sessions, err := m.store.List(ctx)
	if err != nil {
		return
	}
	for _, sess := range sessions {
		if sess == nil || sess.ProjectID != projectID {
			continue
		}
		_ = m.store.UpdateSession(ctx, sess.ID, func(s *api.Session) {
			s.WorkspacePath = ""
		})
	}
}
