package lifecycle

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	"github.com/lycaon/lycaon/pkg/api"
	"log/slog"
	"strings"
)

type StopRepairKey struct{}

func WithStopRepair(ctx context.Context) context.Context {
	return context.WithValue(ctx, StopRepairKey{}, true)
}

func StopRepair(ctx context.Context) bool {
	repair, _ := ctx.Value(StopRepairKey{}).(bool)
	return repair
}

// StopSession cancels executable runs and preserves the ambient root.
func (m *Commands) StopSession(ctx context.Context, sessionID, reason string) error {
	if m == nil || m.Runs == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session_id required")
	}
	guard := m.Admission.Guard(sessionID)
	guard.Lock()
	respawnAmbient, err := m.stopSessionRunsLocked(ctx, sessionID, reason)
	guard.Unlock()
	if err != nil {
		return err
	}
	if !respawnAmbient {
		return nil
	}
	if _, err := m.Ambient.EnsureSessionWorkflow(WithStopRepair(ctx), sessionID); err != nil {
		// Report ambient repair failure to the stop barrier.
		slog.ErrorContext(ctx, "respawn ambient run after session stop", "session_id", sessionID, "err", err)
		return err
	}
	return nil
}

func (m *Commands) stopSessionRunsLocked(ctx context.Context, sessionID, reason string) (bool, error) {
	for {
		active, err := m.Runs.ActiveBySession(ctx, sessionID)
		if err != nil {
			return false, err
		}
		if active == nil {
			return true, nil
		}
		if runstate.IsAmbientRun(active) {
			return false, nil
		}
		root := m.RootRun(ctx, active)
		if runstate.IsAmbientRun(root) {
			canceled, err := m.CancelRun(ctx, active.ID, active.Revision, reason, true)
			if err != nil {
				return false, err
			}
			if _, err := m.Settlement.ResumeParentAfterChildExit(ctx, canceled, string(api.WorkflowRunStatusCanceled)); err != nil {
				return false, err
			}
			continue
		}

		posture := m.BaselinePosture(ctx, root, api.SessionPostureSpec)
		teardowns, err := m.Cleanup.ForSession(ctx, sessionID, reason)
		if err != nil {
			return false, err
		}
		runs, err := m.Trees.CancelActiveTree(ctx, root, reason, posture, teardowns)
		if err != nil {
			return false, err
		}
		for i := range runs {
			m.AfterCanceled(ctx, &runs[i], reason)
		}
		return true, nil
	}
}
