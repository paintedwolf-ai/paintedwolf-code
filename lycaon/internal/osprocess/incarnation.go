package osprocess

// Incarnation names one process across PID reuse.
type Incarnation struct {
	PID     int
	Started int64
}

// CurrentIncarnation identifies pid as it runs now.
func CurrentIncarnation(pid int) (Incarnation, bool) {
	started, ok := StartTime(pid)
	return Incarnation{PID: pid, Started: started}, ok && started > 0
}

// Ended requires positive evidence the recorded process no longer exists: nothing
// answers at the PID, or a later process reused it. An unreadable start time on a
// PID that still answers proves nothing and reports false.
func (i Incarnation) Ended() bool {
	if i.PID <= 0 || i.Started <= 0 {
		return false
	}
	started, ok := StartTime(i.PID)
	if ok {
		return started != i.Started
	}
	return !Alive(i.PID)
}
