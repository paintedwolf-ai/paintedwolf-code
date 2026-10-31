//go:build linux

package exec

import (
	"errors"

	"golang.org/x/sys/unix"
)

// awaitLeaderExit blocks until child pid has exited without reaping it, keeping
// its PID, process group, and session ID reserved while the guard snapshots
// surviving session members.
func awaitLeaderExit(pid int) {
	if pid <= 0 {
		return
	}
	var info unix.Siginfo
	for {
		err := unix.Waitid(unix.P_PID, pid, &info, unix.WEXITED|unix.WNOWAIT, nil)
		if !errors.Is(err, unix.EINTR) {
			return
		}
	}
}
