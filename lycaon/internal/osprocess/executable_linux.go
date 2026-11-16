//go:build linux

package osprocess

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func matchStoppedExecutable(pid int, expected *os.File) (bool, error) {
	root := filepath.Join("/proc", strconv.Itoa(pid))
	if err := requireStopped(root); err != nil {
		return false, err
	}
	observed, err := os.Stat(filepath.Join(root, "exe"))
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrExecutableObservationUnavailable, err)
	}
	held, err := expected.Stat()
	if err != nil {
		return false, err
	}
	if err := requireStopped(root); err != nil {
		return false, err
	}
	return os.SameFile(observed, held), nil
}
