//go:build darwin || linux

package checkpoint

import (
	"errors"
	"golang.org/x/sys/unix"
	"os"
)

// ApplyPathXattrs applies extended attributes to abs, ignoring ENOTSUP/EOPNOTSUPP.
func ApplyPathXattrs(abs string, xattrs map[string][]byte) error {
	for k, v := range xattrs {
		if err := unix.Setxattr(abs, k, v, 0); err != nil && !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EOPNOTSUPP) {
			return err
		}
	}
	return nil
}

func applyFileXattrs(file *os.File, attrs map[string][]byte) error {
	for key, value := range attrs {
		if err := unix.Fsetxattr(int(file.Fd()), key, value, 0); err != nil && !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EOPNOTSUPP) {
			return err
		}
	}
	return nil
}
