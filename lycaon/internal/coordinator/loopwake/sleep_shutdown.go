package loopwake

// StopSleepTimers disarms every armed wait timer at host shutdown. An armed
// timer keeps the engine and its dependencies reachable until it fires. Wait
// leases stay open in the durable store for the next host to settle.
func (l *LoopEngine) StopSleepTimers() {
	if l == nil {
		return
	}
	l.sleep.Range(func(_, value any) bool {
		if st, ok := value.(*sessionSleep); ok && st != nil {
			st.mu.Lock()
			cancelSleepTimerLocked(st)
			st.mu.Unlock()
		}
		return true
	})
}
