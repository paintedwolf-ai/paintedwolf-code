package loopwake

import (
	"time"
)

func (l *Waits) runtimeWaitSubscription(sessionID string) (waitSubscription, bool) {
	value, found := l.sleep.Load(sessionID)
	if !found {
		return waitSubscription{}, false
	}
	state := value.(*sessionSleep)
	state.mu.Lock()
	defer state.mu.Unlock()
	if len(state.waitTriggers) == 0 {
		return waitSubscription{}, false
	}
	for _, trigger := range state.waitTriggers {
		// Readiness parameters are retained only in the durable lease.
		if trigger == WaitTriggerHTTPReady || trigger == WaitTriggerPortReady {
			return waitSubscription{}, false
		}
	}
	_, bounded := waitTriggerSet(state.waitTriggers)[WaitTriggerTimer]
	subscription := waitSubscription{
		UntilComplete:  state.untilComplete,
		Bounded:        bounded,
		Triggers:       append([]WaitTrigger(nil), state.waitTriggers...),
		ProcessHandles: append([]string(nil), state.processHandles...),
		WorkerHandles:  append([]string(nil), state.workerHandles...),
	}
	subscription.Conditions = conditionsFromTriggers(subscription.Triggers, subscription.ProcessHandles, subscription.WorkerHandles)
	return subscription, true
}
func (l *Waits) SessionsSleepingOn(trigger WaitTrigger) []string {
	if l == nil {
		return nil
	}
	now := time.Now()
	var out []string
	l.sleep.Range(func(key, value any) bool {
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
func (l *Waits) SessionSleepingOnProcess(sessionID, handle string) bool {
	if l == nil || !l.IsSleeping(sessionID) {
		return false
	}
	triggers := l.Triggers(sessionID)
	if !waitSubscribesProcessDone(triggers) {
		return false
	}
	return handleMatches(l.ActiveProcessHandles(sessionID), handle)
}
func (l *Waits) Triggers(sessionID string) []WaitTrigger {
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]WaitTrigger(nil), st.waitTriggers...)
}
func (l *Waits) activeSleepMover(sessionID string) SleepMover {
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.mover == SleepMoverUser {
		return SleepMoverUser
	}
	return SleepMoverHost
}
func (l *Waits) activeUntilComplete(sessionID string) bool {
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.untilComplete
}
func (l *Waits) ActiveProcessHandles(sessionID string) []string {
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]string(nil), st.processHandles...)
}
func (l *Waits) activeWorkerHandles(sessionID string) []string {
	st := l.sleep.state(sessionID)
	st.mu.Lock()
	defer st.mu.Unlock()
	return append([]string(nil), st.workerHandles...)
}
