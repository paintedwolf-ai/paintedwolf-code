//go:build darwin

package fssync

import (
	"errors"

	"golang.org/x/sys/unix"
)

// FD flushes a held descriptor to stable storage. Plain fsync on darwin stops at
// the drive's cache, which is the same policy os.File.Sync applies through File.
func FD(fd int) error {
	if relaxed.Load() {
		return nil
	}
	return flushToStableStorage(fd, func(fd int) error {
		_, err := unix.FcntlInt(uintptr(fd), unix.F_FULLFSYNC, 0)
		return err
	}, unix.Fsync)
}

func flushToStableStorage(fd int, full, fallback func(int) error) error {
	for {
		err := full(fd)
		switch {
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.ENOTSUP):
			for {
				if err := fallback(fd); !errors.Is(err, unix.EINTR) {
					return err
				}
			}
		default:
			return err
		}
	}
}
