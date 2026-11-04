//go:build darwin

package fssync

import (
	"errors"
	"os"
	"syscall"
)

// flush is os.File.Sync without its scheduler cost. Sync reaches F_FULLFSYNC through runtime.fcntl,
// which keeps the goroutine's scheduler slot for the whole call. On a view mount served by this
// process the reply needs that slot, or needs a stop-the-world the held slot is blocking, so the
// engine hangs. syscall.Syscall releases the slot.
func flush(f *os.File) error {
	if err := flushFD(int(f.Fd())); err != nil {
		return &os.PathError{Op: "sync", Path: f.Name(), Err: err}
	}
	return nil
}

// flushFD reaches stable storage. Plain fsync stops at the drive's cache, so it serves only
// filesystems that report F_FULLFSYNC unsupported.
func flushFD(fd int) error {
	return flushToStableStorage(fd, func(fd int) error {
		if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_FULLFSYNC, 0); errno != 0 {
			return errno
		}
		return nil
	}, syscall.Fsync)
}

func flushToStableStorage(fd int, full, fallback func(int) error) error {
	for {
		err := full(fd)
		switch {
		case errors.Is(err, syscall.EINTR):
			continue
		case errors.Is(err, syscall.ENOTSUP):
			for {
				if err := fallback(fd); !errors.Is(err, syscall.EINTR) {
					return err
				}
			}
		default:
			return err
		}
	}
}
