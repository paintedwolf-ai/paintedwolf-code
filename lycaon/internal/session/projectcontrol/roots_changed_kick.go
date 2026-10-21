package projectcontrol

import (
	"context"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
)

// EnqueueRootsChangedKick records a project root change and queues one
// notice per open session of the project for its next turn boundary. An
// unchanged root set records and notifies nothing.
func (m *Service) EnqueueRootsChangedKick(ctx context.Context, projectID string, before, after []projectroot.RootRef) {
	if m == nil {
		return
	}
	change, changed := m.rootHistory.Record(projectID, before, after)
	if !changed || m.store == nil || m.anchors == nil {
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
		data := prompts.RootsChangedKickData(change, after, sess.WorkspaceRootID)
		m.anchors.Emit(ctx, sess.ID, anchor.ProjectRootsChanged, anchor.Envelope{Vars: data})
	}
}
