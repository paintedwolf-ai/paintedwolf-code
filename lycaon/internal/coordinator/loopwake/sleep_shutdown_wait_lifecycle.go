package loopwake

func (l *Waits) StopSleepTimers() {
	if l == nil {
		return
	}
	l.sleep.stopTimers()
}
