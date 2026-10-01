package session

import (
	"context"
)

// WithSessionTreeStop commits a reviewed exit before draining its runtime.
func (m *Manager) WithSessionTreeStop(ctx context.Context, sessionID, reason string, transition func(context.Context) error) error {
	stopCtx := context.WithoutCancel(ctx)
	if _, err := m.store.Get(stopCtx, sessionID); err != nil {
		return err
	}
	rootID := m.sessionRootID(stopCtx, sessionID)
	flight, err := m.stopState.Commit(stopCtx, rootID, transition)
	if err != nil {
		return err
	}
	preserveQueueSessionID := ""
	if m.hasAddressedQueuedTurn(stopCtx, sessionID) {
		preserveQueueSessionID = sessionID
	}
	return m.runSessionStopLeader(stopCtx, rootID, flight, reason, sessionStopOptions{
		preserveQueueSessionID:        preserveQueueSessionID,
		transitionedWorkflowSessionID: sessionID,
	})
}
