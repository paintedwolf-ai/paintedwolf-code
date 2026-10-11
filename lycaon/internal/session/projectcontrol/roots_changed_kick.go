package projectcontrol

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
)

// EnqueueRootsChangedKick notifies open sessions when project roots change tier or primary.
func (m *Service) EnqueueRootsChangedKick(ctx context.Context, projectID string, before, after []projectroot.RootRef) {
	if m == nil || m.store == nil || !prompts.RootsChangedKickNeeded(before, after) {
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
