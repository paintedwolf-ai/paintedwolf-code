//go:build !darwin && !linux && !windows

package projectsource

import "os"

func sourceChangeTime(_ *os.File, _ os.FileInfo) ([2]int64, error) {
	return [2]int64{}, ErrSourceNotFound
}
