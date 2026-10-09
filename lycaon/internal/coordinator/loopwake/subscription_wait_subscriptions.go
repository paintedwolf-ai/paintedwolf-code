package loopwake

import (
	"context"
	"github.com/lycaon/lycaon/internal/coordinator/anchor"
	"time"
)

func (l *WaitSubscriptions) SessionsSleepingOn(trigger WaitTrigger) []string {
	if l == nil {
		return nil
	}
	now := time.Now()
	var out []string
	l.Waits.sleep.Range(func(key, value any) bool {
		sessionID, ok := key.(string)
		if !ok {
			return true
		}
		st, ok := value.(*sessionSleep)
		if !ok || st == nil {
			return true
		}
		st.mu.Lock()
		sleeping := sleepArmedLocked(st, now)
		subscribed := false
		for _, t := range st.waitTriggers {
			if t == trigger {
				subscribed = true
				break
			}
		}
		st.mu.Unlock()
		if sleeping && subscribed {
			out = append(out, sessionID)
		}
		return true
	})
	return out
}

func (l *WaitSubscriptions) SessionSleepingOnProcess(sessionID, handle string) bool {
	if l == nil || !l.Waits.IsSleeping(sessionID) {
		return false
	}
	triggers := l.Triggers(sessionID)
	if !waitSubscribesProcessDone(triggers) {
		return false
	}
	return handleMatches(l.ActiveProcessHandles(sessionID), handle)
}

func (l *WaitSubscriptions) scanCycleOpen(ctx context.Context, sessionID string) bool {
	if l == nil {
		return false
	}
	deps := l.loopDeps()
	if deps.ScanCycleOpen == nil {
		return false
	}
	return deps.ScanCycleOpen(ctx, sessionID)
}

func (l *WaitSubscriptions) processCycleOpen(sessionID string, handles []string) bool {
	if l == nil {
		return false
	}
	deps := l.loopDeps()
	if deps.ProcessRunning == nil {
		return false
	}
	return deps.ProcessRunning(sessionID, handles)
}

func (l *WaitSubscriptions) Triggers(sessionID string) []WaitTrigger {
	st := l.Waits.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]WaitTrigger(nil), st.waitTriggers...)
}

func (l *WaitSubscriptions) activeSleepMover(sessionID string) SleepMover {
	st := l.Waits.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.mover == SleepMoverUser {
		return SleepMoverUser
	}
	return SleepMoverHost
}

func (l *WaitSubscriptions) activeUntilComplete(sessionID string) bool {
	st := l.Waits.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.untilComplete
}

func (l *WaitSubscriptions) ActiveProcessHandles(sessionID string) []string {
	st := l.Waits.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]string(nil), st.processHandles...)
}

func (l *WaitSubscriptions) activeWorkerHandles(sessionID string) []string {
	st := l.Waits.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]string(nil), st.workerHandles...)
}

func (l *WaitSubscriptions) overlayPromoteDue(ctx context.Context, sessionID string, env anchor.Envelope) bool {
	if env.HasPendingOverlayPromote() {
		return true
	}
	deps := l.loopDeps()
	if deps.HostWakeOverlayPromoteDue == nil {
		return false
	}
	return deps.HostWakeOverlayPromoteDue(ctx, sessionID)
}

func (l *WaitSubscriptions) waitWakeAccepted(
	ctx context.Context,
	sessionID string,
	wake, inform anchor.ID,
	legID string,
	completingJobID string,
	env anchor.Envelope,
) bool {
	if isAlwaysWakeInform(inform) || isAlwaysWakeNudge(wake) {
		return true
	}
	if !l.Waits.IsSleeping(sessionID) {
		return true
	}
	triggers := l.Triggers(sessionID)
	in := waitMatchInput{
		Wake:              wake,
		CompletingJobID:   completingJobID,
		ProcessHandle:     legID,
		ProcessHandles:    l.ActiveProcessHandles(sessionID),
		WorkerHandles:     l.activeWorkerHandles(sessionID),
		CycleIdle:         l.Cycles.workerCycleIdle(ctx, sessionID, completingJobID),
		OverlayPromoteDue: l.overlayPromoteDue(ctx, sessionID, env),
		NeedsDecision:     env.HasWorkerDecision(),
	}
	return waitEventMatches(triggers, in)
}
