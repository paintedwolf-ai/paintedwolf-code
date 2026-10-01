//go:build !windows

package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// tryLockFile takes a non-blocking exclusive flock. Reports false when another
// process holds it, so the caller can bound its own wait.
func tryLockFile(f *os.File) (bool, error) {
	return tryLockFileMode(f, unix.LOCK_EX)
}

func tryLockFileShared(f *os.File) (bool, error) {
	return tryLockFileMode(f, unix.LOCK_SH)
}

func tryLockFileMode(f *os.File, mode int) (bool, error) {
	err := unix.Flock(int(f.Fd()), mode|unix.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.EWOULDBLOCK):
		return false, nil
	default:
		return false, err
	}
}

func unlockFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
