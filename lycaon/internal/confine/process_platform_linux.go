//go:build linux

package confine

import (
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func nativeFileChange(info os.FileInfo) (fileChange, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileChange{}, ErrLaunchIdentityChanged
	}
	return fileChange{stat.Ctim.Sec, stat.Ctim.Nsec, stat.Uid, stat.Gid}, nil
}

func openNativeLaunchMetadata(path string, directory bool) (*os.File, error) {
	flags := unix.O_PATH | unix.O_CLOEXEC
	if directory {
		flags |= unix.O_DIRECTORY
	}
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}

func nativeArgumentBudget() (int, error) {
	var stack unix.Rlimit
	if err := unix.Getrlimit(unix.RLIMIT_STACK, &stack); err != nil {
		return 0, err
	}
	return linuxArgumentBudget(stack.Cur, os.Getpagesize()), nil
}
func linuxArgumentBudget(stack uint64, pageBytes int) int {
	return max(32*pageBytes, int(min(stack/4, uint64(6<<20))))
}
