//go:build !(darwin && cgo) && !linux

package osprocess

import "os"

func matchProcessDirectory(int, *os.File) (bool, error) {
	return false, ErrExecutableObservationUnavailable
}
func processSecurity(int) (ProcessSecurity, error) {
	return ProcessSecurity{}, ErrExecutableObservationUnavailable
}
func processArguments(int, int) ([]string, []string, error) {
	return nil, nil, ErrExecutableObservationUnavailable
}
