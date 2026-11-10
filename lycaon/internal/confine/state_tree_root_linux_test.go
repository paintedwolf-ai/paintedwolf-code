//go:build linux

package confine_test

import (
	"testing"

	"golang.org/x/sys/unix"
)

func stateTreeHelper(t *testing.T) string {
	t.Helper()
	return requireLiveBoundary(t)
}

func platformSwapRename(oldpath, newpath string) error {
	return unix.Renameat2(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_EXCHANGE)
}

func platformNoReplaceRename(oldpath, newpath string) error {
	return unix.Renameat2(unix.AT_FDCWD, oldpath, unix.AT_FDCWD, newpath, unix.RENAME_NOREPLACE)
}
