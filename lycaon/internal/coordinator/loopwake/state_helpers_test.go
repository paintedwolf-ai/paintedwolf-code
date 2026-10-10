package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"strings"
	"time"
)

func interruptedUntilForTest(l *Waits, sessionID string) time.Time {
	if l == nil {
		return time.Time{}
	}
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.interruptedUntil
}
func sleepReasonForTest(l *Waits, sessionID string) string {
	if l == nil {
		return ""
	}
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.reason
}
func sleepUntilForTest(l *Waits, sessionID string) time.Time {
	if l == nil {
		return time.Time{}
	}
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.until
}
func waitSubscriptionForTest(l *WaitSubscriptions, sessionID string) []WaitTrigger {
	if l == nil {
		return nil
	}
	st := l.Waits.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]WaitTrigger(nil), st.waitTriggers...)
}
func waitLeaseOpenForTest(l *Waits, sessionID string) bool {
	if l == nil {
		return false
	}
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return strings.TrimSpace(st.activityID) != ""
}
func evaluateForTest(l *Admission, ctx context.Context, sessionID string, wake anchor.ID) (allow bool, busy bool) {
	return l.evaluate(ctx, sessionID, wake)
}
