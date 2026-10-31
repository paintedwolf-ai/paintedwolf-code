package osprocess

// Incarnation pairs a process ID with the kernel start timestamp that
// distinguishes it from later processes assigned the same PID.
type Incarnation struct {
	PID   int
	Start int64
}

// IncarnationOf captures the current kernel identity of pid.
func IncarnationOf(pid int) (Incarnation, bool) {
	start, ok := StartTime(pid)
	if !ok || start <= 0 {
		return Incarnation{}, false
	}
	return Incarnation{PID: pid, Start: start}, true
}

// Alive reports whether inc still names a running process with the recorded
// start timestamp.
func (inc Incarnation) Alive() bool {
	if inc.PID <= 0 || inc.Start <= 0 {
		return false
	}
	start, ok := StartTime(inc.PID)
	return ok && start == inc.Start && Alive(inc.PID)
}
