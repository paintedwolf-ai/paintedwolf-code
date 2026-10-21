package projectcontrol

import (
	"context"
	"slices"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
)

type latestAnchorProbe interface {
	HasLatest(sessionID string, id anchor.ID) bool
}

type droppableAnchors interface {
	Drop(sessionID string, id anchor.ID)
}

// EnqueueRootsChangedKick records a project root change and queues one
// notice per open session of the project for its next turn boundary. If
// another change arrives before that turn runs, the pending notice is
// extended from the session's pre-change baseline rather than adding a
// second. An unchanged root set records and notifies nothing.
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
	probe, hasProbe := m.anchors.(latestAnchorProbe)
	for _, sess := range sessions {
		if sess == nil || sess.ProjectID != projectID {
			continue
		}
		baseline := before
		if hasProbe && probe.HasLatest(sess.ID, anchor.ProjectRootsChanged) {
			if saved, ok := m.pendingRootBaselines.Load(sess.ID); ok {
				baseline = saved.([]projectroot.RootRef)
			}
		} else {
			m.pendingRootBaselines.Store(sess.ID, append([]projectroot.RootRef(nil), before...))
		}
		sessionChange := change
		if !slices.Equal(baseline, before) {
			sessionChange = projectroot.DiffRoots(baseline, after)
			sessionChange.Generation = change.Generation
		}
		if sessionChange.Empty() {
			m.pendingRootBaselines.Delete(sess.ID)
			if drop, ok := m.anchors.(droppableAnchors); ok {
				drop.Drop(sess.ID, anchor.ProjectRootsChanged)
			}
			continue
		}
		data := prompts.RootsChangedKickData(sessionChange, after, sess.WorkspaceRootID)
		m.anchors.Emit(ctx, sess.ID, anchor.ProjectRootsChanged, anchor.Envelope{Vars: data})
	}
}
