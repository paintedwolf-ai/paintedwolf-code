//go:build windows

package filelock

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// tryLockFile takes a non-blocking exclusive byte-range lock. Reports false when
// another process holds it, so the caller can bound its own wait.
func tryLockFile(f *os.File) (bool, error) {
	return tryLockFileMode(f, true)
}

func tryLockFileShared(f *os.File) (bool, error) {
	return tryLockFileMode(f, false)
}

func tryLockFileMode(f *os.File, exclusive bool) (bool, error) {
	var overlapped windows.Overlapped
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		flags,
		0, 1, 0, &overlapped,
	)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		return false, nil
	default:
		return false, err
	}
}

func unlockFile(f *os.File) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
}
