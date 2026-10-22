package projectcontrol

import (
	"context"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/gate"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/internal/prompts"
)

type latestAnchorProbe interface {
	HasLatest(sessionID string, id anchor.ID) bool
}

type droppableAnchors interface {
	Drop(sessionID string, id anchor.ID)
}

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

// ObserveSessionPosture records a session's effective approval posture and
// queues one posture-change notice for its next turn boundary when a change
// from its previously observed posture alters whether writes outside the
// project ask first. Repeated changes before that turn runs coalesce against
// the session's pre-change baseline, and reverting to the same outside-folder
// write behavior drops the pending notice.
func (m *Service) ObserveSessionPosture(ctx context.Context, sessionID string, current gate.Posture) {
	sessionID = strings.TrimSpace(sessionID)
	if m == nil || sessionID == "" {
		return
	}
	prevAny, hadPrev := m.lastSessionPosture.Swap(sessionID, current)
	if !hadPrev || m.anchors == nil {
		return
	}
	before := prevAny.(gate.Posture)
	if before == current {
		return
	}
	probe, hasProbe := m.anchors.(latestAnchorProbe)
	baseline := before
	if hasProbe && probe.HasLatest(sessionID, anchor.ProjectPostureChanged) {
		if saved, ok := m.pendingPostureBaselines.Load(sessionID); ok {
			baseline = saved.(gate.Posture)
		}
	} else if prompts.PostureChangedKickNeeded(before, current) {
		m.pendingPostureBaselines.Store(sessionID, before)
	}
	if !prompts.PostureChangedKickNeeded(baseline, current) {
		m.pendingPostureBaselines.Delete(sessionID)
		if drop, ok := m.anchors.(droppableAnchors); ok {
			drop.Drop(sessionID, anchor.ProjectPostureChanged)
		}
		return
	}
	data := prompts.PostureChangedKickData(current)
	m.anchors.Emit(ctx, sessionID, anchor.ProjectPostureChanged, anchor.Envelope{Vars: data})
}

// EnqueuePostureChangedKick queues one posture-change notice per open session
// of the project for its next turn boundary when the change alters whether
// writes outside the project ask first. Repeated changes before that turn runs
// coalesce against the session's pre-change baseline, and reverting to the
// same outside-folder write behavior drops the pending notice.
func (m *Service) EnqueuePostureChangedKick(ctx context.Context, projectID string, before, after gate.Posture) {
	if m == nil || m.store == nil || m.anchors == nil || before == after {
		return
	}
	sessions, err := m.store.List(ctx)
	if err != nil {
		return
	}
	for _, sess := range sessions {
		if sess == nil || (projectID != "" && sess.ProjectID != projectID) {
			continue
		}
		m.lastSessionPosture.Store(sess.ID, before)
		m.ObserveSessionPosture(ctx, sess.ID, after)
	}
}
