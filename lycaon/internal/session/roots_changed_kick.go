package session

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
	"github.com/lycaon/lycaon/pkg/api"
)

func (m *Manager) workspaceRootsForPrompt(ctx context.Context, sess *api.Session) ([]map[string]any, int, string) {
	if m == nil || sess == nil {
		return nil, 0, ""
	}
	refs, err := m.sessionRootRefs(ctx, sess)
	if err != nil {
		return nil, 0, ""
	}
	rows := make([]map[string]any, 0, len(refs))
	for _, r := range refs {
		rows = append(rows, map[string]any{
			"label":      r.Label,
			"path":       r.Path,
			"is_primary": r.IsPrimary,
		})
	}
	activePath := ""
	if r, err := projectroot.ActiveRoot(refs, sess.WorkspaceRootID); err == nil {
		activePath = r.Path
	} else if r, err := projectroot.PrimaryRoot(refs); err == nil {
		activePath = r.Path
	}
	return rows, len(refs), activePath
}

func (m *Manager) ungatedOverlayRootPaths(ctx context.Context, sess *api.Session) []string {
	if m == nil || sess == nil {
		return nil
	}
	refs, err := m.sessionRootRefs(ctx, sess)
	if err != nil {
		return nil
	}
	resolution, err := project.ResolveOverlay(refs, sess.WorkspaceRootID)
	if err != nil {
		return nil
	}
	return resolution.Paths
}

// EnqueueRootsChangedKick notifies open sessions when project roots change tier or primary.
func (m *Manager) EnqueueRootsChangedKick(ctx context.Context, projectID string, before, after []projectroot.RootRef) {
	if m == nil || m.store == nil || !prompts.RootsChangedKickNeeded(before, after) {
		return
	}
	sessions, err := m.store.List(ctx)
	if err != nil {
		return
	}
	rt := m.ensureCoordinatorRuntime()
	for _, sess := range sessions {
		if sess == nil || sess.ProjectID != projectID {
			continue
		}
		data := prompts.RootsChangedKickData(before, after, sess.WorkspaceRootID)
		rt.Anchors().Emit(ctx, sess.ID, anchor.ProjectRootsChanged, anchor.Envelope{Vars: data})
	}
}
