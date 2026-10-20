//go:build linux

package confine

import (
	"context"
	"os"

	"github.com/lycaon/lycaon/internal/osprocess"
	"golang.org/x/sys/unix"
)

// The release barrier remains unavailable until the helper transports and
// validates the actual target identity. Native vector accounting alone does not
// activate required-pin execution or alter privileged host launches.
func pinnedPlatformAvailable() error { return osprocess.ErrExecutableObservationUnavailable }
func awaitPinnedStop(context.Context, int) error {
	return osprocess.ErrExecutableObservationUnavailable
}
func resumePinnedProcess(*os.Process) error { return osprocess.ErrExecutableObservationUnavailable }
func execPinnedTarget(string, []string, int, int) error {
	return osprocess.ErrExecutableObservationUnavailable
}

// Linux derives the combined argv/environment allowance from the inherited soft
// stack limit, with its32-page floor and three-quarter8MiB architecture cap.
// Kernel execution still decides actual admissibility, including per-string and
// pointer overhead; this preflight is not successful native qualification.
func nativeArgumentBudget() (int, error) {
	var stack unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_STACK, &stack); err != nil {
		return 0, err
	}
	return linuxArgumentBudget(stack.Cur, os.Getpagesize()), nil
}
func linuxArgumentBudget(stack uint64, pageBytes int) int {
	return max(32*pageBytes, int(min(stack/4, uint64(6<<20))))
}
