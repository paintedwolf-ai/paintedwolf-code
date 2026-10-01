//go:build darwin || linux

package fseffect

import (
	"os"

	"github.com/lycaon/lycaon/internal/fspath"
	"golang.org/x/sys/unix"
)

func openGuardedDirectory(loc Location, expected string) (*os.File, error) {
	parent, err := openParent(loc, false, 0)
	if err != nil {
		return nil, err
	}
	defer parent.close()
	fd, err := unix.Openat(int(parent.file.Fd()), parent.name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), parent.name)
	identity, err := fspath.EntryIdentityAt(fd, ".")
	if err == nil && (expected == "" || identity != expected) {
		err = ErrPostcondition
	}
	if err != nil {
		_ = dir.Close()
		return nil, err
	}
	return dir, nil
}
