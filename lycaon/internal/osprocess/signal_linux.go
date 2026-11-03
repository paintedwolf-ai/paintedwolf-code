//go:build linux

package osprocess

import (
	"syscall"

	"golang.org/x/sys/unix"
)

// Signal delivers sig to inc only if the kernel confirms that inc.PID still
// has start timestamp inc.Start. Opening a pidfd before reading StartTime pins
// the kernel task_struct across the identity check and signal delivery.
func Signal(inc Incarnation, sig syscall.Signal) error {
	if inc.PID <= 0 || inc.Start <= 0 {
		return syscall.ESRCH
	}
	pidfd, err := unix.PidfdOpen(inc.PID, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(pidfd) }()
	start, ok := StartTime(inc.PID)
	if !ok || start != inc.Start {
		return syscall.ESRCH
	}
	return unix.PidfdSendSignal(pidfd, sig, nil, 0)
}

// SignalGroup delivers sig to the process group led by leader only while an
// open pidfd on leader.PID confirms its start timestamp still matches. Once
// the leader is reaped or replaced, callers must signal surviving member
// incarnations individually.
func SignalGroup(leader Incarnation, sig syscall.Signal) error {
	if leader.PID <= 0 || leader.Start <= 0 {
		return syscall.ESRCH
	}
	pidfd, err := unix.PidfdOpen(leader.PID, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(pidfd) }()
	start, ok := StartTime(leader.PID)
	if !ok || start != leader.Start {
		return syscall.ESRCH
	}
	return syscall.Kill(-leader.PID, sig)
}
