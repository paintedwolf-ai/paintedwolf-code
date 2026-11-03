package adapter

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
	"golang.org/x/sys/unix"
)

var readPathXattrsHook func(path string) error

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

func writePathXattrs(p string, xattrs map[string][]byte) {
	for k, v := range xattrs {
		_ = unix.Lsetxattr(p, k, v, 0)
	}
}

func cloneBaseCaptureRecord(b layeredfs.BaseCapture) layeredfs.BaseCapture {
	b.Xattrs = cloneXattrMap(b.Xattrs)
	return b
}

func cloneXattrMap(in map[string][]byte) map[string][]byte {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string][]byte, len(in))
	for k, v := range in {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

func xattrsEqual(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || !bytes.Equal(va, vb) {
			return false
		}
	}
	return true
}

func applyXattrDiff(core *layeredfs.Core, qp layeredfs.QualifiedPath, diskXattrs map[string][]byte) error {
	existingNames, err := core.ListXattr(qp, true)
	if err != nil {
		return err
	}
	existingSet := make(map[string]struct{}, len(existingNames))
	for _, name := range existingNames {
		existingSet[name] = struct{}{}
		if _, keep := diskXattrs[name]; !keep {
			if err := core.RemoveXattr(qp, name, true); err != nil {
				return err
			}
		}
	}
	for name, val := range diskXattrs {
		if _, had := existingSet[name]; had {
			oldVal, gErr := core.GetXattr(qp, name, true)
			if gErr == nil && bytes.Equal(oldVal, val) {
				continue
			}
		}
		if err := core.SetXattr(qp, name, val, true); err != nil {
			return err
		}
	}
	return nil
}

func isMissingOnDisk(err error) bool {
	return os.IsNotExist(err) || errors.Is(err, syscall.ENOTDIR)
}

func entryKindMatchesMode(kind layeredfs.EntryKind, mode fs.FileMode) bool {
	switch kind {
	case layeredfs.EntryDirectory:
		return mode.IsDir()
	case layeredfs.EntrySymlink:
		return mode&os.ModeSymlink != 0
	case layeredfs.EntryRegular:
		return mode.IsRegular()
	default:
		return false
	}
}
