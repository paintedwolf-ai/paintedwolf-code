//go:build !darwin && !linux

package confine

import (
	"errors"
	"os"
)

var errNativeLaunchMetadataUnavailable = errors.New("native launch metadata is unavailable")

func nativeFileChange(os.FileInfo) (fileChange, error) {
	return fileChange{}, errNativeLaunchMetadataUnavailable
}

func openNativeLaunchMetadata(string, bool) (*os.File, error) {
	return nil, errNativeLaunchMetadataUnavailable
}

func nativeArgumentBudget() (int, error) { return 0, errNativeLaunchMetadataUnavailable }
