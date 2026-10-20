//go:build linux

package confine

import (
	"os"
	"syscall"
)

type fileChange struct {
	Seconds, Nanoseconds int64
	UID, GID             uint32
}

func nativeFileChange(info os.FileInfo) (fileChange, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileChange{}, ErrLaunchIdentityChanged
	}
	return fileChange{stat.Ctim.Sec, stat.Ctim.Nsec, stat.Uid, stat.Gid}, nil
}
