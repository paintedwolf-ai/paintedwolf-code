//go:build darwin

package exec

import (
	"errors"

	"golang.org/x/sys/unix"
)

// darwinZombieStat is XNU's SZOMB process status in kinfo_proc.kp_proc.p_stat.
const darwinZombieStat = 5

// awaitLeaderExit blocks until child pid has exited without reaping it, keeping
// its PID, process group, and session ID reserved while the guard snapshots
// surviving session members.
func awaitLeaderExit(pid int) {
	if pid <= 0 {
		return
	}
	kq, err := unix.Kqueue()
	if err != nil {
		return
	}
	defer func() { _ = unix.Close(kq) }()

	change := [1]unix.Kevent_t{{
		Ident:  uint64(pid),
		Filter: unix.EVFILT_PROC,
		Flags:  unix.EV_ADD | unix.EV_ENABLE | unix.EV_ONESHOT,
		Fflags: unix.NOTE_EXIT,
	}}
	var events [1]unix.Kevent_t
	n, err := unix.Kevent(kq, change[:], events[:], &unix.Timespec{})
	if err != nil || n > 0 || leaderAlreadyExited(pid) {
		return
	}
	for {
		_, err = unix.Kevent(kq, nil, events[:], nil)
		if !errors.Is(err, unix.EINTR) {
			return
		}
	}
}

func leaderAlreadyExited(pid int) bool {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	return err != nil || info == nil || int(info.Proc.P_pid) != pid || info.Proc.P_stat == darwinZombieStat
}
