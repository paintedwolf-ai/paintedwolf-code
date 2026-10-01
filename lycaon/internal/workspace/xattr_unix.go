//go:build darwin || linux

package workspace

import (
	"bytes"
	"errors"

	"golang.org/x/sys/unix"
)

func copyExtendedMetadata(src, dst string) error {
	size, err := unix.Listxattr(src, nil)
	if ignorableXattrError(err) || size == 0 {
		return nil
	}
	if err != nil {
		return err
	}
	names := make([]byte, size)
	size, err = unix.Listxattr(src, names)
	if ignorableXattrError(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, rawName := range bytes.Split(names[:size], []byte{0}) {
		if len(rawName) == 0 {
			continue
		}
		name := string(rawName)
		valueSize, err := unix.Getxattr(src, name, nil)
		if ignorableXattrError(err) {
			continue
		}
		if err != nil {
			return err
		}
		value := make([]byte, valueSize)
		valueSize, err = unix.Getxattr(src, name, value)
		if ignorableXattrError(err) {
			continue
		}
		if err != nil {
			return err
		}
		if err := unix.Setxattr(dst, name, value[:valueSize], 0); err != nil && !ignorableXattrError(err) {
			return err
		}
	}
	return nil
}

func ignorableXattrError(err error) bool {
	return err == nil || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.ENODATA) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES)
}
