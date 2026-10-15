//go:build linux

package osprocess

import "golang.org/x/sys/unix"

// DisableDumps makes this process non-dumpable: other processes of the same
// user can no longer ptrace it or read its memory, environment, or maps
// through the process filesystem, and the kernel writes no core for it.
func DisableDumps() error {
	return unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0)
}
