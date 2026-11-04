//go:build darwin || linux

package fseffect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lycaon/lycaon/internal/fspath"
	"golang.org/x/sys/unix"
)

func openGuardedDirectory(loc Location, expected string) (*os.File, error) {
	parent, err := openParent(loc, false, 0)
	if err != nil {
		return nil, err
	}
	defer parent.close()
	fd, err := unix.Openat(parent.fd, parent.name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
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

// MkdirAll creates every missing component without traversing symlinks.
func MkdirAll(loc Location, mode os.FileMode) (createdCount int, err error) {
	clean, err := cleanLocation(loc)
	if err != nil {
		return createdCount, err
	}
	root, err := openRoot(clean.Root)
	if err != nil {
		return createdCount, err
	}
	defer root.close()
	currentFD := root.fd
	ownsCurrent := false
	defer func() {
		if ownsCurrent {
			_ = unix.Close(currentFD)
		}
	}()
	anyCreated := false
	for _, component := range strings.Split(filepath.ToSlash(clean.Rel), "/") {
		created := false
		next, openErr := unix.Openat(currentFD, component,
			unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if errors.Is(openErr, unix.ENOENT) {
			mkdirErr := unix.Mkdirat(currentFD, component, uint32(mode.Perm()))
			if mkdirErr == nil {
				created = true
				createdCount++
			} else if !errors.Is(mkdirErr, unix.EEXIST) {
				return createdCount, mkdirErr
			}
			next, openErr = unix.Openat(currentFD, component,
				unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		}
		if openErr != nil {
			if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
				return createdCount, fmt.Errorf("%w: %q", ErrSymlink, component)
			}
			return createdCount, openErr
		}
		if created {
			anyCreated = true
			if err := fsyncFD(currentFD); err != nil {
				_ = unix.Close(next)
				return createdCount, err
			}
		}
		if ownsCurrent {
			_ = unix.Close(currentFD)
		}
		currentFD = next
		ownsCurrent = true
	}
	if !anyCreated {
		return createdCount, nil
	}
	return createdCount, fsyncFD(currentFD)
}
