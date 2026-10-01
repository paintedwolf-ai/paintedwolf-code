//go:build darwin || linux

package fseffect

import (
	"errors"
	"os"

	"github.com/lycaon/lycaon/internal/fssync"
	"golang.org/x/sys/unix"
)

// fsyncFD flushes a held descriptor under the process durability policy.
func fsyncFD(fd int) error {
	if fssync.Relaxed() {
		return nil
	}
	return unix.Fsync(fd)
}

// SyncDirectory flushes a staged directory after its children have been written.
func SyncDirectory(root *os.Root, path string) error {
	dir, err := root.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(fssync.File(dir), dir.Close())
}
