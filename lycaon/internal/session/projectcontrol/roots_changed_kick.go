package projectcontrol

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/bgprocess"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
)

// EnqueueRootsChangedKick notifies open sessions when project roots change tier or primary.
func (m *Service) EnqueueRootsChangedKick(ctx context.Context, projectID string, before, after []projectroot.RootRef) {
	if m == nil {
		return
	}
	if m.processes != nil && len(before) > 0 {
		remaining := make(map[string]struct{}, len(after))
		for _, r := range after {
			remaining[r.ID] = struct{}{}
		}
		sessionIDs := m.projectSessionIDs(ctx, projectID)
		for _, r := range before {
			if _, ok := remaining[r.ID]; !ok && strings.TrimSpace(r.Path) != "" {
				_ = m.processes.Lifecycle.StopUnderRoot(ctx, projectID, sessionIDs, r.Path, nil, bgprocess.TerminationRootDetached)
			}
		}
	}
	if m.store == nil || !prompts.RootsChangedKickNeeded(before, after) {
		return
	}
	sessions, err := m.store.List(ctx)
	if err != nil {
		return
	}
	anchors := m.anchors
	for _, sess := range sessions {
		if sess == nil || sess.ProjectID != projectID {
			continue
		}
		data := prompts.RootsChangedKickData(before, after, sess.WorkspaceRootID)
		anchors.Emit(ctx, sess.ID, anchor.ProjectRootsChanged, anchor.Envelope{Vars: data})
	}
}
