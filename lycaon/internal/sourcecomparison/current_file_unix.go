//go:build !windows

package sourcecomparison

import "os"

func currentSnapshotFile() (*os.File, error) {
	file, err := os.CreateTemp("", "source-snapshot-*")
	if err != nil {
		return nil, err
	}
	if err := os.Remove(file.Name()); err != nil {
		_ = file.Close()
		return nil, err
	}
	return file, nil
}
