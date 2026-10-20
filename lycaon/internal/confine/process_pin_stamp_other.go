//go:build !darwin && !linux

package confine

import (
	"github.com/lycaon/lycaon/internal/osprocess"
	"os"
)

type fileChange struct{}

func nativeFileChange(os.FileInfo) (fileChange, error) {
	return fileChange{}, osprocess.ErrExecutableObservationUnavailable
}
