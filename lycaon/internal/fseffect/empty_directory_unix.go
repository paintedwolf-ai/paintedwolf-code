//go:build darwin || linux

package fseffect

import (
	"errors"
	"github.com/lycaon/lycaon/internal/fspath"
	"golang.org/x/sys/unix"
)

func RemoveEmptyDirectoryGuarded(loc Location, expected string) error {
	parent, err := openParent(loc, false, 0)
	if err != nil {
		return err
	}
	defer parent.close()
	identity, err := fspath.EntryIdentityAt(parent.fd, parent.name)
	if err != nil {
		return err
	}
	if expected == "" || identity != expected {
		return ErrPostcondition
	}
	var info unix.Stat_t
	if err := unix.Fstatat(parent.fd, parent.name, &info, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if info.Mode&unix.S_IFMT != unix.S_IFDIR {
		return ErrPostcondition
	}
	if err := unix.Unlinkat(parent.fd, parent.name, unix.AT_REMOVEDIR); err != nil {
		if errors.Is(err, unix.ENOTEMPTY) || errors.Is(err, unix.EEXIST) {
			return ErrDirectoryNotEmpty
		}
		return err
	}
	return fsyncFD(parent.fd)
}
