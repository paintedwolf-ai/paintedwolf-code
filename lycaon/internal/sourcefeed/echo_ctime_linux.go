//go:build linux

package sourcefeed

import (
	"os"
	"syscall"
)

func statChangeTime(info os.FileInfo) ([2]int64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return [2]int64{}, false
	}
	return [2]int64{int64(stat.Ctim.Sec), int64(stat.Ctim.Nsec)}, true
}
