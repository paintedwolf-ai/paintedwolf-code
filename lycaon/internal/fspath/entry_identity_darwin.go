//go:build darwin

package fspath

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// EntryIdentity identifies an entry without following its final symbolic link.
func EntryIdentity(path string) (string, error) { return EntryIdentityAt(unix.AT_FDCWD, path) }

// EntryIdentityAt binds identity lookup to a held parent directory.
func EntryIdentityAt(parent int, path string) (string, error) {
	var stat unix.Stat_t
	if err := unix.Fstatat(parent, path, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d:%d:%d", stat.Dev, stat.Ino, stat.Btim.Sec, stat.Btim.Nsec), nil
}

func SameFilesystem(from, to string) (bool, error) {
	var a, b unix.Stat_t
	if err := unix.Lstat(from, &a); err != nil {
		return false, err
	}
	if err := unix.Stat(to, &b); err != nil {
		return false, err
	}
	return a.Dev == b.Dev, nil
}
