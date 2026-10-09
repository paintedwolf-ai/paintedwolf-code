package loopwake

import (
	"context"
	"fmt"
)

func (l *WaitSubscriptions) CloseCompletedWorkflowWait(ctx context.Context, sessionID string) (bool, error) {
	deps := l.loopDeps()
	if deps.WorkerCycleIdle != nil {
		if deps.GetSession == nil {
			return false, fmt.Errorf("session lookup not configured")
		}
		sess, err := deps.GetSession(ctx, sessionID)
		if err != nil {
			return false, err
		}
		if sess == nil {
			return false, fmt.Errorf("session %q not found", sessionID)
		}
		idle, err := deps.WorkerCycleIdle(ctx, sess, "")
		if err != nil || !idle {
			return false, err
		}
	}
	if l.Facts.sessionHasPendingUserInput(ctx, sessionID) {
		return false, nil
	}
	if store := l.durableWaitStore(); store != nil {
		_, armed, err := store.ForSession(ctx, sessionID)
		if err != nil || armed {
			return false, err
		}
	}
	l.Waits.breakSleep(ctx, sessionID, "workflow_complete", false)
	st := l.Waits.sleep.state(sessionID)
	st.mu.Lock()
	st.waitThisTurn = false
	st.mu.Unlock()
	return true, nil
}
