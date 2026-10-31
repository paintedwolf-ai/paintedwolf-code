//go:build linux

package fseffect

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// renameat2 is the kernel call; a test stands in a filesystem without
// no-replace renames.
var renameat2 = unix.Renameat2

// renameNoReplace moves an entry only if nothing occupies the destination.
// NFS, and FUSE filesystems whose server lacks it, refuse RENAME_NOREPLACE;
// there a regular file or link is published by an exclusive hard link, which
// fails atomically on an occupant, and a directory by a plain rename after an
// occupancy check, which can at worst replace an empty directory created in
// between: the kernel refuses to rename over a non-empty directory or a
// non-directory.
func renameNoReplace(from int, name string, to int, dest string) error {
	err := renameat2(from, name, to, dest, unix.RENAME_NOREPLACE)
	if !errors.Is(err, unix.EINVAL) && !errors.Is(err, unix.ENOSYS) && !errors.Is(err, unix.EOPNOTSUPP) {
		return err
	}
	var source unix.Stat_t
	if statErr := unix.Fstatat(from, name, &source, unix.AT_SYMLINK_NOFOLLOW); statErr != nil {
		return statErr
	}
	if source.Mode&unix.S_IFMT == unix.S_IFDIR {
		var occupant unix.Stat_t
		switch statErr := unix.Fstatat(to, dest, &occupant, unix.AT_SYMLINK_NOFOLLOW); {
		case statErr == nil:
			return unix.EEXIST
		case !errors.Is(statErr, unix.ENOENT):
			return statErr
		}
		return unix.Renameat(from, name, to, dest)
	}
	if linkErr := unix.Linkat(from, name, to, dest, 0); linkErr != nil {
		if errors.Is(linkErr, unix.EEXIST) {
			return linkErr
		}
		return fmt.Errorf("filesystem renames neither without replacing nor by hard link: %w", errors.Join(err, linkErr))
	}
	if unlinkErr := unix.Unlinkat(from, name, 0); unlinkErr != nil {
		_ = unix.Unlinkat(to, dest, 0)
		return unlinkErr
	}
	return nil
}

func renameExchange(from int, name string, to int, dest string) error {
	return unix.Renameat2(from, name, to, dest, unix.RENAME_EXCHANGE)
}

// utimeOmit leaves one timestamp unchanged in utimensat.
const utimeOmit = unix.UTIME_OMIT

func platformAccessTime(info os.FileInfo) (time.Time, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(st.Atim.Sec), int64(st.Atim.Nsec)), true
}
