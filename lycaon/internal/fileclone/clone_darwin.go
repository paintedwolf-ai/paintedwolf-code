//go:build darwin

package fileclone

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Clone attempts a filesystem copy-on-write clone, reporting unsupported filesystems without error.
func Clone(src, dst string) (bool, error) {
	file, err := openCloneSource(src)
	if err != nil {
		return false, err
	}
	defer func() { _ = file.Close() }()
	err = unix.Fclonefileat(int(file.Fd()), unix.AT_FDCWD, dst, 0)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.EXDEV), errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOSYS):
		return false, nil
	default:
		return false, err
	}
}

// CloneInto creates a regular-file clone beneath a held staging root.
func CloneInto(source *os.File, destination *os.Root, path string) (bool, error) {
	parent, err := destination.Open(filepath.Dir(path))
	if err != nil {
		return false, err
	}
	defer func() { _ = parent.Close() }()
	err = unix.Fclonefileat(int(source.Fd()), int(parent.Fd()), filepath.Base(path), 0)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.EXDEV), errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOSYS):
		return false, nil
	default:
		return false, err
	}
}
