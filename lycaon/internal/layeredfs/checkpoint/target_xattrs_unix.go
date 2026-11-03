//go:build darwin || linux

package checkpoint

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"syscall"

	"github.com/lycaon/lycaon/internal/fspath"
	"golang.org/x/sys/unix"
)

func targetFileIdentity(file *os.File, info os.FileInfo) (string, error) {
	id, _, err := targetFileIdentityAndLinks(file, info)
	return id, err
}

func targetFileIdentityAndLinks(file *os.File, info os.FileInfo) (string, uint64, error) {
	if info != nil {
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			return fmt.Sprintf("%d:%d", uint64(st.Dev), uint64(st.Ino)), uint64(st.Nlink), nil
		}
		observed := fspath.ObserveFile(info)
		if observed.Known {
			return fmt.Sprintf("%d:%d", observed.Identity.Device, observed.Identity.Inode), 1, nil
		}
	}
	id, err := fspath.EntryIdentity(file.Name())
	return id, 1, err
}

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
	current, _ := readFileXattrs(file)
	for key, value := range attrs {
		if existing, ok := current[key]; ok && bytes.Equal(existing, value) {
			continue
		}
		if err := unix.Fsetxattr(int(file.Fd()), key, value, 0); err != nil && !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EOPNOTSUPP) {
			return err
		}
	}
	return nil
}

func readFileXattrs(file *os.File) (map[string][]byte, error) {
	size, err := unix.Flistxattr(int(file.Fd()), nil)
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EOPNOTSUPP) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if size == 0 {
		return nil, nil
	}
	names := make([]byte, size)
	size, err = unix.Flistxattr(int(file.Fd()), names)
	if err != nil {
		return nil, err
	}
	attrs := map[string][]byte{}
	for _, name := range bytes.Split(names[:size], []byte{0}) {
		if len(name) == 0 {
			continue
		}
		key := string(name)
		size, err := unix.Fgetxattr(int(file.Fd()), key, nil)
		if err != nil {
			return nil, err
		}
		value := make([]byte, size)
		size, err = unix.Fgetxattr(int(file.Fd()), key, value)
		if err != nil {
			return nil, err
		}
		attrs[key] = value[:size]
	}
	return attrs, nil
}

func restoreFileXattrs(file *os.File, attrs map[string][]byte) error {
	current, err := readFileXattrs(file)
	if err != nil {
		return err
	}
	for key := range current {
		if _, keep := attrs[key]; !keep {
			if err := unix.Fremovexattr(int(file.Fd()), key); err != nil {
				return err
			}
		}
	}
	return applyFileXattrs(file, attrs)
}
