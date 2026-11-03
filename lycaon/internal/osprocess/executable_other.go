//go:build !(darwin && cgo) && !linux

package osprocess

import "os"

func matchStoppedExecutable(int, *os.File) (bool, error) {
	return false, ErrExecutableObservationUnavailable
}
