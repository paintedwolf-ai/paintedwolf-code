//go:build !darwin && !linux

package workerplacement

import (
	"errors"
	"os"
)

func volumeOf(string) (Volume, error) {
	return Volume{}, errors.New("volume identity is not available on this platform")
}

func readOnlyFilesystem(error) bool { return false }

func fallbackParents() []string { return []string{os.TempDir()} }

func excludeFromBackupsAndIndexing(string) error { return nil }
