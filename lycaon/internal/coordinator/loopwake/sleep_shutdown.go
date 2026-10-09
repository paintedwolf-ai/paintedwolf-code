package loopwake

import "sync"

// sessionSleeps maps a session ID to its *sessionSleep.
type sessionSleeps struct{ sync.Map }

// state returns the session's sleep state, creating it on first use.
func (s *sessionSleeps) state(sessionID string) *sessionSleep {
	if v, ok := s.Load(sessionID); ok {
		if st, ok := v.(*sessionSleep); ok && st != nil {
			return st
		}
	}
	st := &sessionSleep{}
	actual, _ := s.LoadOrStore(sessionID, st)
	if existing, ok := actual.(*sessionSleep); ok && existing != nil {
		return existing
	}
	return st
}

// stopTimers disarms every armed wait timer.
func (s *sessionSleeps) stopTimers() {
	s.Range(func(_, value any) bool {
		if st, ok := value.(*sessionSleep); ok && st != nil {
			st.mu.Lock()
			cancelSleepTimerLocked(st)
			st.mu.Unlock()
		}
		return true
	})
}

// StopSleepTimers disarms every armed wait timer at host shutdown. An armed
// timer keeps the engine and its dependencies reachable until it fires. Wait
// leases stay open in the durable store for the next host to settle.
func (l *LoopEngine) StopSleepTimers() {
	if l == nil {
		return
	}
	l.sleep.stopTimers()
}
