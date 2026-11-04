//go:build linux

package fssync

import (
	"errors"

	"golang.org/x/sys/unix"
)

// FD flushes a held descriptor to stable storage.
func FD(fd int) error {
	if relaxed.Load() {
		return nil
	}
	for {
		if err := unix.Fsync(fd); !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
