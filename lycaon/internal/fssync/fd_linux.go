//go:build linux

package fssync

import (
	"errors"

	"golang.org/x/sys/unix"
)

func flushFD(fd int) error {
	for {
		if err := unix.Fsync(fd); !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}
