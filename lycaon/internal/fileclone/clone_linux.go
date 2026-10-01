//go:build linux

package fileclone

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// Clone attempts a filesystem copy-on-write clone, reporting unsupported filesystems without error.
func Clone(src, dst string) (bool, error) {
	in, err := openCloneSource(src)
	if err != nil {
		return false, err
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	cloneErr := unix.IoctlFileClone(int(out.Fd()), int(in.Fd()))
	closeErr := out.Close()
	if cloneErr == nil && closeErr == nil {
		return true, nil
	}
	if err := os.Remove(dst); err != nil {
		return false, errors.Join(cloneErr, closeErr, err)
	}
	if cloneErr == nil {
		return false, closeErr
	}
	switch {
	case errors.Is(cloneErr, unix.EXDEV), errors.Is(cloneErr, unix.ENOTSUP), errors.Is(cloneErr, unix.EOPNOTSUPP), errors.Is(cloneErr, unix.EINVAL), errors.Is(cloneErr, unix.ENOTTY), errors.Is(cloneErr, unix.ENOSYS):
		return false, nil
	default:
		return false, cloneErr
	}
}

func CloneInto(source *os.File, destination *os.Root, path string) (bool, error) {
	out, err := destination.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return false, err
	}
	cloneErr := unix.IoctlFileClone(int(out.Fd()), int(source.Fd()))
	closeErr := out.Close()
	if cloneErr == nil && closeErr == nil {
		return true, nil
	}
	if err := destination.Remove(path); err != nil {
		return false, errors.Join(cloneErr, closeErr, err)
	}
	if cloneErr == nil {
		return false, closeErr
	}
	switch {
	case errors.Is(cloneErr, unix.EXDEV), errors.Is(cloneErr, unix.ENOTSUP), errors.Is(cloneErr, unix.EINVAL), errors.Is(cloneErr, unix.ENOTTY), errors.Is(cloneErr, unix.ENOSYS):
		return false, nil
	default:
		return false, cloneErr
	}
}
