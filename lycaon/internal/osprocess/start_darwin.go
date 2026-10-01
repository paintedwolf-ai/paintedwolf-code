//go:build darwin

package osprocess

import "golang.org/x/sys/unix"

// StartTime identifies one incarnation of pid in microseconds since the epoch.
// A reused pid reports a later start, so equality proves the same process.
func StartTime(pid int) (int64, bool) {
	if pid <= 0 {
		return 0, false
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil || int(info.Proc.P_pid) != pid {
		return 0, false
	}
	start := info.Proc.P_starttime
	return int64(start.Sec)*1_000_000 + int64(start.Usec), true
}
