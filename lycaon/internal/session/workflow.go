package session

import (
	"context"
)

// workflowfacts.ActiveWorkflowManifest holds runtime fields from the active workflow manifest.

func (m *Manager) assertWorkflowRunnable(ctx context.Context, sessionID string) error {
	if m == nil || m.workflows == nil {
		return nil
	}
	return m.workflows.AssertSessionRunnable(ctx, sessionID)
}

func (m *Manager) hasActiveWorkflowRun(ctx context.Context, sessionID string) bool {
	if m == nil || m.workflows == nil {
		return false
	}
	run, err := m.workflows.GetActive(ctx, sessionID)
	return err == nil && run != nil
}
