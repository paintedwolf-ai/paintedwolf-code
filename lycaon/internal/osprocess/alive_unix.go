//go:build unix

package osprocess

import (
	"errors"
	"syscall"
)

// Alive reports whether pid names a running process. Signal 0 probes without
// delivering; EPERM means the process exists but is another user's. A
// non-positive pid addresses a process group, never a single process.
func Alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
