//go:build !darwin && !linux

package confine

import (
	"github.com/lycaon/lycaon/internal/osprocess"
	"os"
)

func nativeFileChange(os.FileInfo) (fileChange, error) {
	return fileChange{}, osprocess.ErrExecutableObservationUnavailable
}

func openNativeLaunchMetadata(string, bool) (*os.File, error) {
	return nil, osprocess.ErrExecutableObservationUnavailable
}
