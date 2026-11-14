//go:build linux

package fspath

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// EntryIdentity is stable across renames and descendant edits.
func EntryIdentity(path string) (string, error) { return EntryIdentityAt(unix.AT_FDCWD, path) }

func EntryIdentityAt(parent int, path string) (string, error) {
	return entryIdentityAt(parent, path, unix.AT_SYMLINK_NOFOLLOW)
}

// FileIdentity is EntryIdentity for an entry already held open.
func FileIdentity(file *os.File) (string, error) {
	return entryIdentityAt(int(file.Fd()), "", unix.AT_EMPTY_PATH)
}

func entryIdentityAt(parent int, path string, flags int) (string, error) {
	var stat unix.Statx_t
	err := unix.Statx(parent, path, flags, unix.STATX_INO|unix.STATX_BTIME, &stat)
	if err == nil && stat.Mask&unix.STATX_BTIME != 0 {
		return fmt.Sprintf("%d:%d:%d:%d:%d", stat.Dev_major, stat.Dev_minor, stat.Ino, stat.Btime.Sec, stat.Btime.Nsec), nil
	}
	if err != nil && !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EINVAL) {
		return "", err
	}
	return identityWithoutBirthTime(parent, path, flags)
}

// identityWithoutBirthTime names an entry on a filesystem that reports no
// birth time.
func identityWithoutBirthTime(parent int, path string, flags int) (string, error) {
	var legacy unix.Stat_t
	if err := unix.Fstatat(parent, path, &legacy, flags); err != nil {
		return "", err
	}
	// Without birth time (NFS, older ext4), the filesystem's handle carries the
	// inode generation, so a reused inode number names a different entry. A
	// filesystem without handles (some FUSE servers) keeps device and inode;
	// ctime cannot serve, because it changes on rename.
	if handle, _, err := unix.NameToHandleAt(parent, path, flags&unix.AT_EMPTY_PATH); err == nil {
		return fmt.Sprintf("%d:%d:h%d:%x", legacy.Dev, legacy.Ino, handle.Type(), handle.Bytes()), nil
	}
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

// identityWithoutDevice drops the leading major:minor of a birth-time identity,
// or the single device field of the other two forms.
func identityWithoutDevice(identity string) string {
	fields := strings.Split(identity, ":")
	if len(fields) == 5 {
		return strings.Join(fields[2:], ":")
	}
	return strings.Join(fields[1:], ":")
}
