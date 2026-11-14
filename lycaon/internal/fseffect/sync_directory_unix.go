//go:build darwin || linux

package fseffect

import (
	"errors"
	"os"

	"github.com/lycaon/lycaon/internal/fssync"
)

// fsyncFD flushes a held descriptor under the process durability policy.
func fsyncFD(fd int) error {
	return fssync.FD(fd)
}

// SyncDirectory flushes a staged directory after its children have been written.
func SyncDirectory(root *os.Root, path string) error {
	dir, err := root.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(fssync.File(dir), dir.Close())
}
