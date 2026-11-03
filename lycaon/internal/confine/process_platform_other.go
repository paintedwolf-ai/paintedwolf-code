//go:build !darwin && !linux

package confine

import (
	"os"

	"github.com/lycaon/lycaon/internal/osprocess"
)

func nativeFileChange(os.FileInfo) (fileChange, error) {
	return fileChange{}, osprocess.ErrExecutableObservationUnavailable
}

func openNativeLaunchMetadata(string, bool) (*os.File, error) {
	return nil, osprocess.ErrExecutableObservationUnavailable
}

func nativeArgumentBudget() (int, error) { return 0, osprocess.ErrExecutableObservationUnavailable }
