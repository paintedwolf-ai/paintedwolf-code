//go:build darwin || linux

package fseffect

import (
	"errors"
	"io"
	"os"

	"github.com/lycaon/lycaon/internal/fspath"
	"github.com/lycaon/lycaon/internal/fssync"
	"golang.org/x/sys/unix"
)

// RemoveTreeGuarded removes a quarantined entry through held directory descriptors.
func RemoveTreeGuarded(loc Location, expected string) error {
	parent, err := openParent(loc, false, 0)
	if err != nil {
		return err
	}
	defer parent.close()
	identity, err := fspath.EntryIdentityAt(int(parent.file.Fd()), parent.name)
	if err != nil {
		return err
	}
	if expected == "" || identity != expected {
		return ErrPostcondition
	}
	if err := removeTreeAt(int(parent.file.Fd()), parent.name); err != nil {
		return err
	}
	return fssync.File(parent.file)
}

func removeTreeAt(parent int, name string) error {
	var stat unix.Stat_t
	if err := unix.Fstatat(parent, name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return unix.Unlinkat(parent, name, 0)
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	dir := os.NewFile(uintptr(fd), name)
	defer func() { _ = dir.Close() }()
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		return err
	}
	if stat.Dev != opened.Dev || stat.Ino != opened.Ino {
		return ErrPostcondition
	}
	// The destination retains the quarantined tree.
	if opened.Mode&0o700 != 0o700 {
		if err := unix.Fchmod(fd, uint32(opened.Mode)|0o700); err != nil {
			return err
		}
	}
	for {
		names, err := dir.Readdirnames(256)
		for _, child := range names {
			if err := removeTreeAt(fd, child); err != nil {
				return err
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	if err := fsyncFD(fd); err != nil {
		return err
	}
	return unix.Unlinkat(parent, name, unix.AT_REMOVEDIR)
}
