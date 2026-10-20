//go:build darwin

package confine

import (
	"os"
	"syscall"
)

func nativeFileChange(info os.FileInfo) (fileChange, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileChange{}, ErrLaunchIdentityChanged
	}
	return fileChange{stat.Ctimespec.Sec, stat.Ctimespec.Nsec, stat.Uid, stat.Gid}, nil
}
