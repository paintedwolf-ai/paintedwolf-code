//go:build unix && !linux

package exec

import "syscall"

// processNice reads a process's nice value.
func processNice(pid int) (int, error) {
	return syscall.Getpriority(syscall.PRIO_PROCESS, pid)
}
