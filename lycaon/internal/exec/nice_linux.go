package exec

import "syscall"

// processNice reads a process's nice value. Linux's getpriority returns
// 20 minus the nice value so that its result is never negative.
func processNice(pid int) (int, error) {
	raw, err := syscall.Getpriority(syscall.PRIO_PROCESS, pid)
	return 20 - raw, err
}
