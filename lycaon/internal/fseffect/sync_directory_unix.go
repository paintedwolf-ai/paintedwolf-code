//go:build darwin || linux

package fseffect

import (
	"errors"
	"os"

	"github.com/lycaon/lycaon/internal/fssync"
)

// SyncDirectory flushes a staged directory after its children have been written.
func SyncDirectory(root *os.Root, path string) error {
	dir, err := root.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(fssync.File(dir), dir.Close())
}
