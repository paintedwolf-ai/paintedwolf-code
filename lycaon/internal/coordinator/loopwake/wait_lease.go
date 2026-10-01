package loopwake

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SleepMover identifies who can end an armed sleep.
type SleepMover string

const (
	// SleepMoverHost holds an activity lease until a host event or timer fires.
	SleepMoverHost SleepMover = "host"
	// SleepMoverUser waits without an activity lease.
	SleepMoverUser SleepMover = "user"
)

// OwesFutureWork reports whether the host will act on this sleep unprompted.
func (m SleepMover) OwesFutureWork() bool { return m == SleepMoverHost }

// WaitLease is one edge of the armed-sleep activity lease.
type WaitLease struct {
	ActivityID string
	SessionID  string
	Active     bool
	StartedAt  time.Time
	Triggers   []WaitTrigger
}

// publishWaitLease publishes one lease edge when configured.
func (l *LoopEngine) publishWaitLease(ctx context.Context, lease WaitLease) {
	if l == nil || strings.TrimSpace(lease.ActivityID) == "" {
		return
	}
	publish := l.loopDeps().PublishWaitLease
	if publish == nil {
		return
	}
	publish(ctx, lease.SessionID, lease)
}

// openWaitLeaseLocked opens a lease for a host-mover sleep.
func openWaitLeaseLocked(st *sessionSleep, sessionID string, mover SleepMover) WaitLease {
	if st == nil || !mover.OwesFutureWork() {
		return WaitLease{}
	}
	st.activityID = uuid.NewString()
	st.activityStartedAt = time.Now().UTC()
	return WaitLease{
		ActivityID: st.activityID,
		SessionID:  sessionID,
		Active:     true,
		StartedAt:  st.activityStartedAt,
		Triggers:   append([]WaitTrigger(nil), st.waitTriggers...),
	}
}

// closeWaitLeaseLocked returns a terminal edge for publication after unlocking.
func closeWaitLeaseLocked(st *sessionSleep, sessionID string) (WaitLease, bool) {
	if st == nil || strings.TrimSpace(st.activityID) == "" {
		return WaitLease{}, false
	}
	lease := WaitLease{
		ActivityID: st.activityID,
		SessionID:  sessionID,
		Active:     false,
		StartedAt:  st.activityStartedAt,
		Triggers:   append([]WaitTrigger(nil), st.waitTriggers...),
	}
	st.activityID = ""
	st.activityStartedAt = time.Time{}
	return lease, true
}

// WaitLeaseOpenForTest reports whether an armed-sleep lease is open.
func (l *LoopEngine) WaitLeaseOpenForTest(sessionID string) bool {
	if l == nil {
		return false
	}
	st := l.sleepState(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return strings.TrimSpace(st.activityID) != ""
}
