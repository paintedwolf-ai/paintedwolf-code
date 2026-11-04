//go:build !darwin && !linux

package workerplacement

import (
	"errors"
	"os"
	"path/filepath"
)

func volumeOf(string) (Volume, error) {
	return Volume{}, errors.New("volume identity is not available on this platform")
}

func readOnlyFilesystem(error) bool { return false }

func fallbackParents() []string { return nil }

func filesystemIsVolatile(string) bool { return false }

func volumeIgnoresOwnership(string) bool { return false }

func verifyPrivateFolder(path string) error {
	_, err := os.Stat(path)
	return err
}

func readPrivateFile(folder, name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(folder, name))
}

func excludeFromBackupsAndIndexing(string) error { return nil }
