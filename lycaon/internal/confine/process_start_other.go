//go:build !linux && !(darwin && cgo)

package confine

import (
	"context"
	"github.com/lycaon/lycaon/internal/osprocess"
	"os"
	"os/exec"
)

func pinnedPlatformAvailable(*ProcessPins) error {
	return osprocess.ErrExecutableObservationUnavailable
}
func nativeArgumentBudget() (int, error) { return 0, osprocess.ErrExecutableObservationUnavailable }
func awaitPinnedStop(context.Context, int) error {
	return osprocess.ErrExecutableObservationUnavailable
}
func resumePinnedProcess(*os.Process) error { return osprocess.ErrExecutableObservationUnavailable }
func execPinnedTarget(string, []string, int, int) error {
	return osprocess.ErrExecutableObservationUnavailable
}

func newPinnedTransport() (*os.File, *os.File, error)                     { return os.Pipe() }
func attachPlatformPins(*exec.Cmd, *pendingProcessPins) ([]string, error) { return nil, nil }
func confirmPlatformPins(context.Context, *exec.Cmd, *pendingProcessPins) (bool, error) {
	return false, nil
}
func platformLaunchMechanism(*ProcessPins) LaunchMechanism { return UnsupportedLaunch }
