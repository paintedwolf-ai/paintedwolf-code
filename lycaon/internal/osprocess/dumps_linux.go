//go:build linux

package osprocess

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// DisableDumps makes this process non-dumpable: other processes of the same
// user can no longer ptrace it or read its memory, environment, or maps
// through the process filesystem, and the kernel writes no core for it.
func DisableDumps() error {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return err
	}
	mode, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil {
		return err
	}
	if mode != 0 {
		return fmt.Errorf("process remains dumpable (mode %d)", mode)
	}
	return nil
}
