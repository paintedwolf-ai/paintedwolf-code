//go:build darwin

package fileclone

import (
	"errors"

	"golang.org/x/sys/unix"
)

// CloneDirectory clones the tree at src to dst in one call. The clone keeps modes, file times,
// extended attributes, flags, symlinks and sparse files; it drops ACLs, hard-link identity and
// directory times, which the caller restores. A volume that cannot clone reports false.
func CloneDirectory(src, dst string) (bool, error) {
	err := unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, unix.EXDEV), errors.Is(err, unix.ENOTSUP), errors.Is(err, unix.EINVAL), errors.Is(err, unix.ENOSYS), errors.Is(err, unix.ENOTTY):
		return false, nil
	default:
		return false, err
	}
}
