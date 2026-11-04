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
	fd := f.Fd()
	for {
		_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_FULLFSYNC, 0)
		switch {
		case errno == 0:
			return nil
		case errors.Is(errno, syscall.EINTR):
			continue
		case errors.Is(errno, syscall.ENOTSUP):
			return syscall.Fsync(int(fd))
		default:
			return &os.PathError{Op: "sync", Path: f.Name(), Err: errno}
		}
	}
}
