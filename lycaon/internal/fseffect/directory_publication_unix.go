//go:build darwin || linux

package fseffect

import (
	"crypto/rand"
	"encoding/hex"
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

func platformHardLink(req HardLinkRequest) error {
	src, err := cleanLocation(req.Source)
	if err != nil {
		return err
	}
	dst, err := cleanLocation(req.Location)
	if err != nil {
		return err
	}
	unlock := lockTarget(dst)
	defer unlock()

	srcParent, err := openParent(src, false, 0)
	if err != nil {
		return err
	}
	defer srcParent.close()

	var srcStat unix.Stat_t
	if err := unix.Fstatat(srcParent.fd, srcParent.name, &srcStat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if srcStat.Mode&unix.S_IFMT == unix.S_IFLNK {
		return fmt.Errorf("%w: %s", ErrSymlink, src.Rel)
	}
	if srcStat.Mode&unix.S_IFMT != unix.S_IFREG {
		return fmt.Errorf("%w: hard link source is not a regular file", ErrInvalidPath)
	}

	dirMode := req.DirMode.Perm()
	dstParent, err := openParent(dst, dirMode != 0, dirMode)
	if err != nil {
		return err
	}
	defer dstParent.close()

	var dstStat unix.Stat_t
	dstErr := unix.Fstatat(dstParent.fd, dstParent.name, &dstStat, unix.AT_SYMLINK_NOFOLLOW)
	if dstErr == nil && !req.Replace {
		return os.ErrExist
	} else if dstErr != nil && !errors.Is(dstErr, unix.ENOENT) {
		return dstErr
	}

	tmpName, held, err := stageHardLinkAt(srcParent, &srcStat, dstParent)
	if err != nil {
		return err
	}
	defer func() {
		_ = unix.Unlinkat(dstParent.fd, tmpName, 0)
		held.retire()
	}()

	if req.BeforeCommit != nil {
		if err := req.BeforeCommit(targetAt{parent: dstParent}); err != nil {
			return err
		}
	}
	if !req.Replace {
		if err := renameNoReplace(dstParent.fd, tmpName, dstParent.fd, dstParent.name); err != nil {
			if errors.Is(err, unix.EEXIST) {
				return os.ErrExist
			}
			return err
		}
	} else if err := unix.Renameat(dstParent.fd, tmpName, dstParent.fd, dstParent.name); err != nil {
		return err
	}
	return fsyncFD(dstParent.fd)
}

func stageHardLinkAt(srcParent *openedParent, srcStat *unix.Stat_t, dstParent *openedParent) (string, stagingHold, error) {
	directory, err := parentDirectoryIdentity(dstParent)
	if err != nil {
		return "", stagingHold{}, fmt.Errorf("identify hard link parent: %w", err)
	}
	for range 64 {
		var nonce [12]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", stagingHold{}, fmt.Errorf("randomize hard link: %w", err)
		}
		name := "." + dstParent.name + "." + hex.EncodeToString(nonce[:]) + ".tmp"
		held := holdStaging(directory, name)
		err := unix.Linkat(srcParent.fd, srcParent.name, dstParent.fd, name, 0)
		if err != nil {
			held.abandon()
			if errors.Is(err, unix.EEXIST) {
				continue
			}
			return "", stagingHold{}, err
		}
		var stagedStat unix.Stat_t
		if statErr := unix.Fstatat(dstParent.fd, name, &stagedStat, unix.AT_SYMLINK_NOFOLLOW); statErr != nil ||
			stagedStat.Dev != srcStat.Dev || stagedStat.Ino != srcStat.Ino {
			_ = unix.Unlinkat(dstParent.fd, name, 0)
			held.retire()
			if statErr != nil {
				return "", stagingHold{}, statErr
			}
			return "", stagingHold{}, fmt.Errorf("%w: hard link source changed during staging", ErrPostcondition)
		}
		return name, held, nil
	}
	return "", stagingHold{}, fmt.Errorf("create hard link: exhausted unique names")
}
