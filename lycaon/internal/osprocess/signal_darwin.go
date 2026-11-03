//go:build darwin

package osprocess

import "syscall"

// Signal delivers sig to inc only if the kernel confirms that inc.PID still
// has start timestamp inc.Start. macOS has no unprivileged pidfd equivalent,
// so checking StartTime immediately before kill narrows the race window to a
// single syscall pair and refuses delivery once the process has been reaped or
// replaced.
func Signal(inc Incarnation, sig syscall.Signal) error {
	if inc.PID <= 0 || inc.Start <= 0 {
		return syscall.ESRCH
	}
	start, ok := StartTime(inc.PID)
	if !ok || start != inc.Start {
		return syscall.ESRCH
	}
	return syscall.Kill(inc.PID, sig)
}

// SignalGroup delivers sig to the process group led by leader only if the
// kernel confirms immediately before kill that leader.PID still has start
// timestamp leader.Start. macOS has no unprivileged pidfd equivalent, so the
// start-time check narrows the window to a single syscall pair and refuses
// group delivery once the leader has been reaped or replaced.
func SignalGroup(leader Incarnation, sig syscall.Signal) error {
	if leader.PID <= 0 || leader.Start <= 0 {
		return syscall.ESRCH
	}
	start, ok := StartTime(leader.PID)
	if !ok || start != leader.Start {
		return syscall.ESRCH
	}
	return syscall.Kill(-leader.PID, sig)
}
