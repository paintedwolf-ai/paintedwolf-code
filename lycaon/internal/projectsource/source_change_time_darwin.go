package projectsource

import (
	"os"
	"syscall"
)

func sourceChangeTime(_ *os.File, info os.FileInfo) ([2]int64, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return [2]int64{}, ErrSourceNotFound
	}
	return [2]int64{int64(stat.Ctimespec.Sec), int64(stat.Ctimespec.Nsec)}, nil
}
