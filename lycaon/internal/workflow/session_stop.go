package workflow

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/lycaon/lycaon/pkg/api"
)

type workflowStopRepairKey struct{}

func withWorkflowStopRepair(ctx context.Context) context.Context {
	return context.WithValue(ctx, workflowStopRepairKey{}, true)
}

func workflowStopRepair(ctx context.Context) bool {
	repair, _ := ctx.Value(workflowStopRepairKey{}).(bool)
	return repair
}

// StopSession cancels executable runs and preserves the ambient root.
func (m *RunManager) StopSession(ctx context.Context, sessionID, reason string) error {
	if m == nil || m.Store == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session_id required")
	}
	guard := m.startGuardFor(sessionID)
	guard.Lock()
	respawnAmbient, err := m.stopSessionRunsLocked(ctx, sessionID, reason)
	guard.Unlock()
	if err != nil {
		return err
	}
	if !respawnAmbient {
		return nil
	}
	if _, err := m.EnsureSessionWorkflow(withWorkflowStopRepair(ctx), sessionID); err != nil {
		// Report ambient repair failure to the stop barrier.
		slog.ErrorContext(ctx, "respawn ambient run after session stop", "session_id", sessionID, "err", err)
		return err
	}
	return nil
}

func (m *RunManager) stopSessionRunsLocked(ctx context.Context, sessionID, reason string) (bool, error) {
	for {
		active, err := m.Store.ActiveBySession(ctx, sessionID)
		if err != nil {
			return false, err
		}
		if active == nil {
			return true, nil
		}
		if m.IsAmbientRun(active) {
			return false, nil
		}
		root := m.rootRun(ctx, active)
		if m.IsAmbientRun(root) {
			canceled, err := m.cancelRun(ctx, active.ID, active.Revision, reason, true)
			if err != nil {
				return false, err
			}
			if _, err := m.resumeParentAfterChildExit(ctx, canceled, string(api.WorkflowRunStatusCanceled)); err != nil {
				return false, err
			}
			continue
		}

		posture := m.baselinePostureForRun(ctx, root, api.SessionPostureSpec)
		teardowns, err := m.activeTeardownIntents(ctx, sessionID, reason)
		if err != nil {
			return false, err
		}
		runs, err := m.Store.CancelActiveTree(ctx, root, reason, posture, teardowns)
		if err != nil {
			return false, err
		}
		for i := range runs {
			m.afterRunCanceled(ctx, &runs[i], reason)
		}
		return true, nil
	}
}
