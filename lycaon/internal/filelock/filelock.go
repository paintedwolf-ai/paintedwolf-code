// Package filelock provides cross-process advisory file locking.
package filelock

import (
	"fmt"
	"os"
	"path/filepath"
)

// TryExclusive attempts to acquire a non-blocking exclusive lock on f.
func TryExclusive(f *os.File) (bool, error) {
	return tryLockFile(f)
}

// TryShared takes a non-blocking shared lock on f. It reports false with a nil
// error while another process holds an exclusive lock.
func TryShared(f *os.File) (bool, error) {
	return tryLockFileShared(f)
}

// Unlock releases a lock taken by TryExclusive or TryShared. Closing the file
// releases it too; call this when the file outlives the lock.
func Unlock(f *os.File) error {
	return unlockFile(f)
}

// Open opens (creating if needed) an owner-only lock file at path.
func Open(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("filelock: lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- caller-derived lock path
	if err != nil {
		return nil, fmt.Errorf("filelock: open %s: %w", filepath.Base(path), err)
	}
	return f, nil
}
