//go:build !(darwin && cgo)

package confine

import (
	"context"
	"github.com/lycaon/lycaon/internal/osprocess"
	"os"
)

func pinnedPlatformAvailable() error     { return osprocess.ErrExecutableObservationUnavailable }
func nativeArgumentBudget() (int, error) { return 0, osprocess.ErrExecutableObservationUnavailable }
func awaitPinnedStop(context.Context, int) error {
	return osprocess.ErrExecutableObservationUnavailable
}
func resumePinnedProcess(*os.Process) error { return osprocess.ErrExecutableObservationUnavailable }
func execPinnedTarget(string, []string, int, int) error {
	return osprocess.ErrExecutableObservationUnavailable
}
