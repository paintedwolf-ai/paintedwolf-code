package osprocess

import (
	"errors"
	"os"
)

// ErrExecutableObservationUnavailable means executable identity could not be
// established from a stopped process's kernel image. A pathname is no fallback.
var ErrExecutableObservationUnavailable = errors.New("stopped executable identity observation is unavailable")

// MatchStoppedExecutable compares a held executable to the kernel image of a
// stopped process. The launch guard must keep it stopped through this check and
// resume only after all authority/confinement checks; this operation grants none.
// A matching interpreter image does not establish an opened script's identity.
func MatchStoppedExecutable(pid int, expected *os.File) (bool, error) {
	if pid <= 0 || expected == nil {
		return false, ErrExecutableObservationUnavailable
	}
	started, ok := StartTime(pid)
	if !ok {
		return false, ErrExecutableObservationUnavailable
	}
	matches, err := matchStoppedExecutable(pid, expected)
	current, ok := StartTime(pid)
	if !ok || current != started {
		return false, ErrExecutableObservationUnavailable
	}
	return matches, err
}
