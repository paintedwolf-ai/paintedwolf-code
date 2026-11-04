//go:build unix

package adapter

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// MountBoundaryPresent reports whether path is the root of another filesystem. A stat the
// filesystem itself fails counts: a dead or hung mount answers with an error, never data.
func MountBoundaryPresent(path string) (bool, error) {
	info, err := BoundedLstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, nil
	}
	if !info.IsDir() {
		return false, nil
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return true, nil
	}
	own, ownOK := info.Sys().(*syscall.Stat_t)
	above, aboveOK := parent.Sys().(*syscall.Stat_t)
	if !ownOK || !aboveOK {
		return false, errors.New("adapter: no device number for " + path)
	}
	return own.Dev != above.Dev, nil
}
