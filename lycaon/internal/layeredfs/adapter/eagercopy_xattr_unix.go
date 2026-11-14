//go:build darwin || linux

package adapter

import (
	"bytes"
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func readPathXattrs(p string) (map[string][]byte, error) {
	if readPathXattrsHook != nil {
		if err := readPathXattrsHook(p); err != nil {
			return nil, err
		}
	}
	sz, err := unix.Llistxattr(p, nil)
	if err != nil {
		if isIgnorableXattrErr(err) {
			return nil, nil
		}
		return nil, err
	}
	if sz <= 0 {
		return nil, nil
	}
	buf := make([]byte, sz)
	n, err := unix.Llistxattr(p, buf)
	if err != nil {
		if isIgnorableXattrErr(err) {
			return nil, nil
		}
		return nil, err
	}
	if n <= 0 {
		return nil, nil
	}
	out := make(map[string][]byte)
	for _, rawName := range bytes.Split(buf[:n], []byte{0}) {
		if len(rawName) == 0 {
			continue
		}
		name := string(rawName)
		val, ok, gErr := readSinglePathXattr(p, name)
		if gErr != nil {
			return nil, gErr
		}
		if ok {
			out[name] = val
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func readSinglePathXattr(p, name string) ([]byte, bool, error) {
	vsz, err := unix.Lgetxattr(p, name, nil)
	if err != nil {
		if isIgnorableXattrErr(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if vsz < 0 {
		return nil, false, nil
	}
	if vsz == 0 {
		return []byte{}, true, nil
	}
	vbuf := make([]byte, vsz)
	vn, err := unix.Lgetxattr(p, name, vbuf)
	if err != nil {
		if isIgnorableXattrErr(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if vn < 0 {
		return nil, false, nil
	}
	return vbuf[:vn], true, nil
}

func isIgnorableXattrErr(err error) bool {
	return errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.ENOSYS) ||
		errors.Is(err, unix.ENODATA) ||
		errors.Is(err, syscall.Errno(93))
}

func writePathXattrs(p string, xattrs map[string][]byte) error {
	for k, v := range xattrs {
		if writePathXattrsHook != nil {
			if err := writePathXattrsHook(p, k, v); err != nil {
				return fmt.Errorf("write xattr %q on %q: %w", k, p, err)
			}
		}
		if err := unix.Lsetxattr(p, k, v, 0); err != nil {
			return fmt.Errorf("write xattr %q on %q: %w", k, p, err)
		}
	}
	return nil
}
