//go:build darwin

package socketref

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func linkedReferenceDirectory(root, prefix, source string) (string, *os.File, error) {
	directory, err := os.MkdirTemp(root, prefix)
	if err != nil {
		return "", nil, err
	}
	guard, err := os.Open(directory)
	if err != nil {
		_ = os.Remove(directory)
		return "", nil, err
	}
	if err := os.Link(source, filepath.Join(directory, "socket")); err != nil {
		_ = guard.Close()
		_ = os.Remove(directory)
		return "", nil, err
	}
	return directory, guard, nil
}

func captureReferenceDirectory(aliases, source string) (string, *os.File, func() error, error) {
	directory, guard, err := linkedReferenceDirectory(aliases, "service-", source)
	if !errors.Is(err, unix.EXDEV) {
		return directory, guard, func() error { return nil }, err
	}
	root, err := volumeTemporaryDirectory(filepath.Dir(source))
	if err != nil {
		return "", nil, nil, err
	}
	parent, err := os.Open(root)
	if err != nil {
		_ = os.Remove(root)
		return "", nil, nil, err
	}
	var expected unix.Stat_t
	if unix.Fstat(int(parent.Fd()), &expected) != nil || expected.Mode&unix.S_IFMT != unix.S_IFDIR || expected.Uid != uint32(os.Getuid()) {
		_ = parent.Close()
		_ = os.Remove(root)
		return "", nil, nil, ErrUnbound
	}
	releaseParent := func() error {
		defer parent.Close()
		var current unix.Stat_t
		if unix.Lstat(root, &current) != nil || current.Dev != expected.Dev || current.Ino != expected.Ino {
			return ErrUnbound
		}
		return os.Remove(root)
	}
	directory, guard, err = linkedReferenceDirectory(root, shortReferencePrefix(), source)
	if err != nil {
		_ = releaseParent()
		return "", nil, nil, err
	}
	return directory, guard, releaseParent, nil
}
