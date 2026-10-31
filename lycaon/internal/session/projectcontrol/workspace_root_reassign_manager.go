package projectcontrol

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

// ReassignSessionsAfterRootDetach retargets open sessions when a folder root is removed.
func (m *Service) ReassignSessionsAfterRootDetach(ctx context.Context, projectID, detachedRootID string, after []projectroot.RootRef) {
	if m == nil || m.store == nil {
		return
	}
	projectID = strings.TrimSpace(projectID)
	detachedRootID = strings.TrimSpace(detachedRootID)
	if projectID == "" || detachedRootID == "" {
		return
	}
	if m.processes != nil {
		if sessions, err := m.store.List(ctx); err == nil {
			for _, sess := range sessions {
				if sess == nil || sess.ProjectID != projectID {
					continue
				}
				if len(after) == 0 || strings.TrimSpace(sess.WorkspaceRootID) == detachedRootID || m.processes.HasRunning(sess.ID) {
					_ = m.processes.Lifecycle.DisposeSession(ctx, sess.ID, bgprocess.TerminationRootDetached)
				}
			}
		}
	}
	replacement := ""
	if r, err := projectroot.PrimaryRoot(after); err == nil {
		replacement = r.ID
	}
	_ = m.store.ReassignSessionsWorkspaceRoot(ctx, projectID, detachedRootID, replacement)
}

// InvalidateSessionWorkspacePaths clears hydrated workspace paths.
func (m *Service) InvalidateSessionWorkspacePaths(ctx context.Context, projectID string) {
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
