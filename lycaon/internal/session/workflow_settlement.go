package session

import (
	"context"
	"fmt"
)

// SettleCompletedWorkflow releases a finished workflow's visible turn after live execution drains.
func (m *Manager) SettleCompletedWorkflow(ctx context.Context, sessionID, runID string) error {
	if m == nil || sessionID == "" || runID == "" {
		return nil
	}
	m.deferredWorkflowCompletions.Store(sessionID, runID)
	return m.settleDeferredUserTurn(context.WithoutCancel(ctx), sessionID)
}

func (m *Manager) completedWorkflowSettlement(ctx context.Context, sessionID string) (string, bool, error) {
	value, ok := m.deferredWorkflowCompletions.Load(sessionID)
	if !ok {
		return "", false, nil
	}
	runID := value.(string)
	if m.loopWorkflowSource == nil {
		return runID, false, fmt.Errorf("workflow completion lookup not configured")
	}
	active, err := m.loopWorkflowSource.ActiveRun(ctx, sessionID)
	if err != nil {
		return runID, false, err
	}
	if active != nil {
		m.deferredWorkflowCompletions.CompareAndDelete(sessionID, runID)
		return "", false, nil
	}
	return runID, true, nil
}
