package stopping

import (
	"context"
)

// WithSessionTreeStop commits a reviewed exit before draining its runtime.
func (m *Service) WithSessionTreeStop(ctx context.Context, sessionID, reason string, transition func(context.Context) error) error {
	stopCtx := context.WithoutCancel(ctx)
	if _, err := m.store.Get(stopCtx, sessionID); err != nil {
		return err
	}
	rootID := m.gate.RootID(stopCtx, sessionID)
	flight, err := m.gate.Commit(stopCtx, rootID, transition)
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
