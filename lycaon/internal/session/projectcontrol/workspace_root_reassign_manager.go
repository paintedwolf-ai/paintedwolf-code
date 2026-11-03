package projectcontrol

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/project"
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
		sessionIDs := m.projectSessionIDs(ctx, projectID)
		if len(after) == 0 {
			_ = m.processes.Lifecycle.StopUnderRoot(ctx, projectID, sessionIDs, "", nil, bgprocess.TerminationRootDetached)
		} else if m.projects != nil {
			if p, err := m.projects.Get(ctx, projectID); err == nil && p != nil {
				if detached, ok := projectroot.RootRefByID(project.RootRefsFrom(p), detachedRootID); ok && strings.TrimSpace(detached.Path) != "" {
					_ = m.processes.Lifecycle.StopUnderRoot(ctx, projectID, sessionIDs, detached.Path, nil, bgprocess.TerminationRootDetached)
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
