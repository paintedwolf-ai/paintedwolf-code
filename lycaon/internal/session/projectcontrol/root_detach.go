package projectcontrol

import (
	"context"
	"fmt"
	"strings"

	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"github.com/lycaon/lycaon/internal/project"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

const rootDetachCancelReason = "folder removed"

// ForceCancelForRootDetach cleanly stops every dependent before the root row is removed.
func (m *Service) ForceCancelForRootDetach(ctx context.Context, projectID, rootID string, dependents RootDependents, detachedPath string) error {
	if m == nil {
		return fmt.Errorf("session manager not configured")
	}
	projectID = strings.TrimSpace(projectID)
	rootID = strings.TrimSpace(rootID)
	if projectID == "" || rootID == "" {
		return fmt.Errorf("project_id and root_id required")
	}
	reason := rootDetachCancelReason
	if path := strings.TrimSpace(detachedPath); path != "" {
		reason = fmt.Sprintf("%s: %s", rootDetachCancelReason, path)
	}
	return m.forceCancelDependents(ctx, projectID, rootID, dependents, reason, detachedPath)
}

// ForceCancelForProjectDelete stops dependents across every root before project delete.
func (m *Service) ForceCancelForProjectDelete(ctx context.Context, projectID string, dependents RootDependents) error {
	if m == nil {
		return fmt.Errorf("session manager not configured")
	}
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return fmt.Errorf("project_id required")
	}
	return m.forceCancelDependents(ctx, projectID, "", dependents, "project deleted", "")
}

func (m *Service) forceCancelDependents(ctx context.Context, projectID, rootID string, dependents RootDependents, reason, detachedPath string) error {
	var roots []projectroot.RootRef
	if m.projects != nil {
		if p, err := m.projects.Get(ctx, projectID); err == nil && p != nil {
			roots = project.RootRefsFrom(p)
		}
	}
	if m.workerAbort != nil {
		if rootID != "" {
			_ = m.workerAbort.AbortWorkersForRoot(ctx, projectID, rootID, roots, reason)
		} else {
			for _, root := range roots {
				_ = m.workerAbort.AbortWorkersForRoot(ctx, projectID, root.ID, roots, reason)
			}
		}
	}
	for _, overlay := range dependents.Overlays {
		sessionID := strings.TrimSpace(overlay.SessionID)
		overlayID := strings.TrimSpace(overlay.OverlayID)
		if sessionID == "" || overlayID == "" {
			continue
		}
		_, _ = m.RejectOverlay(ctx, sessionID, overlayID, reason)
	}
	for _, sess := range dependents.Sessions {
		sessionID := strings.TrimSpace(sess.SessionID)
		if sessionID == "" {
			continue
		}
		_ = m.stops.Abort(ctx, sessionID, reason)
		m.status.PublishIdle(ctx, sessionID, api.SessionIdleDispositionTurnError)
	}
	m.enqueueRootDetachCanceledKick(ctx, projectID, rootID, detachedPath, dependents)
	return nil
}

func (m *Service) enqueueRootDetachCanceledKick(ctx context.Context, projectID, rootID, detachedPath string, dependents RootDependents) {
	if m == nil || m.store == nil || (len(dependents.Workers) == 0 && len(dependents.Overlays) == 0 && len(dependents.Sessions) == 0) {
		return
	}
	sessions, err := m.store.List(ctx)
	if err != nil {
		return
	}
	workers := make([]map[string]any, 0, len(dependents.Workers))
	for _, w := range dependents.Workers {
		workers = append(workers, map[string]any{
			"job_id":     w.JobID,
			"session_id": w.SessionID,
			"agent_type": w.AgentType,
			"reason":     rootDetachCancelReason,
		})
	}
	overlays := make([]map[string]any, 0, len(dependents.Overlays))
	for _, o := range dependents.Overlays {
		overlays = append(overlays, map[string]any{
			"overlay_id": o.OverlayID,
			"job_id":     o.JobID,
			"session_id": o.SessionID,
		})
	}
	data := map[string]any{
		"root_id":  rootID,
		"path":     detachedPath,
		"workers":  workers,
		"overlays": overlays,
		"reason":   rootDetachCancelReason,
	}
	anchors := m.anchors
	for _, sess := range sessions {
		if sess == nil || sess.ProjectID != projectID {
			continue
		}
		anchors.Emit(ctx, sess.ID, anchor.ProjectRootDetachCanceled, anchor.Envelope{Vars: data})
	}
}
