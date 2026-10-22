package projectcontrol

import (
	"context"
	"slices"
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
	probe, hasProbe := m.anchors.(latestAnchorProbe)
	if hasProbe && !probe.HasLatest(sessionID, anchor.ProjectPostureChanged) {
		m.pendingPostureBaselines.Delete(sessionID)
		m.pendingExternalBaselines.Delete(sessionID)
	}
	before := prevAny.(gate.Posture)
	if before == current {
		return
	}
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
		externalChanged := false
		if savedExt, ok := m.pendingExternalBaselines.Load(sessionID); ok {
			if curExt, ok := m.lastSessionExternalHidden.Load(sessionID); ok {
				externalChanged = savedExt.(bool) != curExt.(bool)
			}
		}
		if !externalChanged {
			if drop, ok := m.anchors.(droppableAnchors); ok {
				drop.Drop(sessionID, anchor.ProjectPostureChanged)
			}
			return
		}
		data := prompts.PostureChangedKickData(current)
		curExt, _ := m.lastSessionExternalHidden.Load(sessionID)
		data["command_external_changed"] = true
		data["command_external_hidden"] = curExt.(bool)
		m.anchors.Emit(ctx, sessionID, anchor.ProjectPostureChanged, anchor.Envelope{Vars: data})
		return
	}
	data := prompts.PostureChangedKickData(current)
	if savedExt, ok := m.pendingExternalBaselines.Load(sessionID); ok {
		if curExt, ok := m.lastSessionExternalHidden.Load(sessionID); ok && savedExt.(bool) != curExt.(bool) {
			data["command_external_changed"] = true
			data["command_external_hidden"] = curExt.(bool)
		}
	}
	m.anchors.Emit(ctx, sessionID, anchor.ProjectPostureChanged, anchor.Envelope{Vars: data})
}

// ObserveSessionCommandExternal records whether the explicit external selector
// is hidden for sessionID and queues or updates the session's
// project.posture.changed notice when external selector visibility flips
// between turns. Repeated toggles before the next turn coalesce against the
// pre-change baseline, and reverting to the baseline visibility drops the
// pending notice when outside-folder write review is also unchanged.
func (m *Service) ObserveSessionCommandExternal(ctx context.Context, sessionID string, currentPosture gate.Posture, currentHidden bool) {
	sessionID = strings.TrimSpace(sessionID)
	if m == nil || sessionID == "" {
		return
	}
	prevAny, hadPrev := m.lastSessionExternalHidden.Swap(sessionID, currentHidden)
	if !hadPrev || m.anchors == nil {
		return
	}
	probe, hasProbe := m.anchors.(latestAnchorProbe)
	if hasProbe && !probe.HasLatest(sessionID, anchor.ProjectPostureChanged) {
		m.pendingPostureBaselines.Delete(sessionID)
		m.pendingExternalBaselines.Delete(sessionID)
	}
	beforeHidden := prevAny.(bool)
	if beforeHidden == currentHidden {
		return
	}
	baselineHidden := beforeHidden
	if hasProbe && probe.HasLatest(sessionID, anchor.ProjectPostureChanged) {
		if saved, ok := m.pendingExternalBaselines.Load(sessionID); ok {
			baselineHidden = saved.(bool)
		} else {
			m.pendingExternalBaselines.Store(sessionID, beforeHidden)
		}
	} else {
		m.pendingExternalBaselines.Store(sessionID, beforeHidden)
	}
	postureChanged := false
	if savedPosture, ok := m.pendingPostureBaselines.Load(sessionID); ok {
		postureChanged = prompts.PostureChangedKickNeeded(savedPosture.(gate.Posture), currentPosture)
	}
	if baselineHidden == currentHidden && !postureChanged {
		m.pendingExternalBaselines.Delete(sessionID)
		if drop, ok := m.anchors.(droppableAnchors); ok {
			drop.Drop(sessionID, anchor.ProjectPostureChanged)
		}
		return
	}
	data := prompts.PostureChangedKickData(currentPosture)
	if baselineHidden != currentHidden {
		data["command_external_changed"] = true
		data["command_external_hidden"] = currentHidden
	}
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
