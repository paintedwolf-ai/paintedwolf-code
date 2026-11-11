//go:build darwin

package osprocess

import "golang.org/x/sys/unix"

// ParentPID is the kernel-recorded parent of pid.
func ParentPID(pid int) (int, bool) {
	if pid <= 0 {
		return 0, false
	}
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info == nil || int(info.Proc.P_pid) != pid {
		return 0, false
	}
	return int(info.Eproc.Ppid), true
}
