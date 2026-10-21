//go:build darwin

package confine

import (
	"golang.org/x/sys/unix"
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

func openNativeLaunchMetadata(path string, directory bool) (*os.File, error) {
	flags := unix.O_EXEC | unix.O_CLOEXEC
	if directory {
		flags = unix.O_SEARCH | unix.O_CLOEXEC
	}
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
