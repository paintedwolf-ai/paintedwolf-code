//go:build linux

package fspath

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

// EntryIdentity is stable across renames and descendant edits.
func EntryIdentity(path string) (string, error) { return EntryIdentityAt(unix.AT_FDCWD, path) }

func EntryIdentityAt(parent int, path string) (string, error) {
	var stat unix.Statx_t
	err := unix.Statx(parent, path, unix.AT_SYMLINK_NOFOLLOW, unix.STATX_INO|unix.STATX_BTIME, &stat)
	if err == nil && stat.Mask&unix.STATX_BTIME != 0 {
		return fmt.Sprintf("%d:%d:%d:%d:%d", stat.Dev_major, stat.Dev_minor, stat.Ino, stat.Btime.Sec, stat.Btime.Nsec), nil
	}
	if err != nil && !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) {
		return "", err
	}
	var legacy unix.Stat_t
	if err := unix.Fstatat(parent, path, &legacy, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return "", err
	}
	// Without birth time, identity uses device and inode; ctime changes on rename.
	return fmt.Sprintf("%d:%d", legacy.Dev, legacy.Ino), nil
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
