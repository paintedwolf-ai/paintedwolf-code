//go:build darwin && !cgo

package osprocess

import "errors"

// ExecutablePath needs libproc, which this build lacks.
func ExecutablePath(int) (string, error) {
	return "", errors.New("process executable path requires cgo on macOS")
}
