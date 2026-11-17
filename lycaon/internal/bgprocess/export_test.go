package bgprocess

// ListenerSweepRunning reports whether the periodic listener sweep loop is live.
func (r *ProcessLifecycle) ListenerSweepRunning() bool {
	r.sweeper.loopMu.Lock()
	defer r.sweeper.loopMu.Unlock()
	return r.sweeper.loopDone != nil
}
