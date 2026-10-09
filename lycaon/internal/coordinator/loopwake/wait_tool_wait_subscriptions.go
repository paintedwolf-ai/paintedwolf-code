package loopwake

func (l *WaitSubscriptions) runtimeWaitSubscription(sessionID string) (waitSubscription, bool) {
	value, found := l.Waits.sleep.Load(sessionID)
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
