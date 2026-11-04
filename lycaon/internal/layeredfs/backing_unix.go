//go:build darwin || linux

package layeredfs

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/lycaon/lycaon/internal/fileclone"
	"github.com/lycaon/lycaon/internal/fssync"
	"golang.org/x/sys/unix"
)

func openDirNoFollow(path string) (*os.File, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(string(filepath.Separator), unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	for _, comp := range strings.Split(strings.TrimPrefix(canonical, string(filepath.Separator)), string(filepath.Separator)) {
		if comp == "" {
			continue
		}
		next, openErr := unix.Openat(fd, comp, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = unix.Close(fd)
		if openErr != nil {
			if errors.Is(openErr, unix.ELOOP) || errors.Is(openErr, unix.ENOTDIR) {
				return nil, ErrSymlinkEscape
			}
			return nil, openErr
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), canonical), nil
}

func mkdirBackingDir(path string, mode fs.FileMode) error {
	return os.MkdirAll(path, mode.Perm())
}

func createExclusiveBackingFile(dirFD int, name, fullPath string, mode fs.FileMode) (*os.File, error) {
	fd, err := unix.Openat(dirFD, name, unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, uint32(mode.Perm()))
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), fullPath), nil
}

func openBackingFileRW(dirFD int, name, fullPath string) (*os.File, error) {
	fd, err := unix.Openat(dirFD, name, unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), fullPath), nil
}

func openBackingFileRO(dirFD int, name, fullPath string) (*os.File, error) {
	fd, err := unix.Openat(dirFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), fullPath), nil
}

func unlinkBackingFile(dirFD int, name string) error {
	err := unix.Unlinkat(dirFD, name, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return err
}

func renameBackingFile(dirFD int, oldName, newName string) error {
	return unix.Renameat(dirFD, oldName, dirFD, newName)
}

func syncFD(fd int) error {
	if fssync.Relaxed() {
		return nil
	}
	err := unix.Fsync(fd)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) {
		return nil
	}
	return err
}

func cloneFileIfSupported(srcPath, dstPath string) (bool, error) {
	return fileclone.Clone(srcPath, dstPath)
}

func listXattrAt(parentFD int, name string) (map[string][]byte, error) {
	if name == "." {
		return listXattrFD(parentFD)
	}
	var stBefore unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stBefore, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if stBefore.Mode&unix.S_IFMT == unix.S_IFLNK {
		return readSymlinkXattrAt(parentFD, name, stBefore)
	}
	openFlags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK
	if stBefore.Mode&unix.S_IFMT == unix.S_IFDIR {
		openFlags |= unix.O_DIRECTORY
	}
	fd, err := unix.Openat(parentFD, name, openFlags, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, ErrSymlinkEscape
		}
		return nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	var stAfter unix.Stat_t
	if err := unix.Fstat(fd, &stAfter); err != nil {
		return nil, err
	}
	if stAfter.Dev != stBefore.Dev || stAfter.Ino != stBefore.Ino || (stAfter.Mode&unix.S_IFMT) != (stBefore.Mode&unix.S_IFMT) {
		return nil, ErrSourceChangedDuringCapture
	}
	return listXattrFD(fd)
}

func listXattrFD(fd int) (map[string][]byte, error) {
	size, err := unix.Flistxattr(fd, nil)
	if err != nil {
		if isIgnorableXattrErr(err) {
			return nil, nil
		}
		return nil, err
	}
	if size <= 0 {
		return nil, nil
	}
	buf := make([]byte, size)
	size, err = unix.Flistxattr(fd, buf)
	if err != nil {
		if isIgnorableXattrErr(err) {
			return nil, nil
		}
		return nil, err
	}
	if size <= 0 {
		return nil, nil
	}
	out := make(map[string][]byte)
	for _, raw := range bytes.Split(buf[:size], []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		attr := string(raw)
		val, ok, gErr := readOneXattrFD(fd, attr)
		if gErr != nil {
			return nil, gErr
		}
		if ok {
			out[attr] = val
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func readOneXattrFD(fd int, attr string) ([]byte, bool, error) {
	vSize, err := unix.Fgetxattr(fd, attr, nil)
	if err != nil {
		if isIgnorableXattrErr(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if vSize < 0 {
		return nil, false, nil
	}
	if vSize == 0 {
		return []byte{}, true, nil
	}
	val := make([]byte, vSize)
	vSize, err = unix.Fgetxattr(fd, attr, val)
	if err != nil {
		if isIgnorableXattrErr(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return val[:vSize], true, nil
}

func listXattrSymlinkProcPath(procPath string) (map[string][]byte, error) {
	size, err := unix.Llistxattr(procPath, nil)
	if err != nil {
		if isIgnorableXattrErr(err) {
			return nil, nil
		}
		return nil, err
	}
	if size <= 0 {
		return nil, nil
	}
	buf := make([]byte, size)
	size, err = unix.Llistxattr(procPath, buf)
	if err != nil {
		if isIgnorableXattrErr(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make(map[string][]byte)
	for _, raw := range bytes.Split(buf[:size], []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		attr := string(raw)
		vSize, gErr := unix.Lgetxattr(procPath, attr, nil)
		if gErr != nil {
			if isIgnorableXattrErr(gErr) {
				continue
			}
			return nil, gErr
		}
		if vSize <= 0 {
			out[attr] = []byte{}
			continue
		}
		val := make([]byte, vSize)
		vSize, gErr = unix.Lgetxattr(procPath, attr, val)
		if gErr != nil {
			if isIgnorableXattrErr(gErr) {
				continue
			}
			return nil, gErr
		}
		out[attr] = val[:vSize]
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func isIgnorableXattrErr(err error) bool {
	return errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.ENOSYS) ||
		errors.Is(err, unix.ENODATA) ||
		errors.Is(err, syscall.Errno(93))
}

func statToLowerEntry(st unix.Stat_t) LowerEntry {
	var kind EntryKind
	var mode fs.FileMode
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		kind = EntryRegular
	case unix.S_IFDIR:
		kind = EntryDirectory
		mode |= fs.ModeDir
	case unix.S_IFLNK:
		kind = EntrySymlink
		mode |= fs.ModeSymlink
	default:
		kind = EntryAbsent
	}
	mode |= fs.FileMode(st.Mode & 0o777)
	if st.Mode&unix.S_ISUID != 0 {
		mode |= fs.ModeSetuid
	}
	if st.Mode&unix.S_ISGID != 0 {
		mode |= fs.ModeSetgid
	}
	if st.Mode&unix.S_ISVTX != 0 {
		mode |= fs.ModeSticky
	}
	mtimeSec, mtimeNsec := statMtime(st)
	ctimeSec, ctimeNsec := statCtime(st)
	return LowerEntry{
		Kind:      kind,
		Mode:      mode,
		Size:      st.Size,
		ModTime:   time.Unix(mtimeSec, mtimeNsec),
		Dev:       uint64(st.Dev),
		Ino:       uint64(st.Ino),
		CtimeSec:  ctimeSec,
		CtimeNsec: ctimeNsec,
		UID:       st.Uid,
		GID:       st.Gid,
		Nlink:     uint32(st.Nlink),
	}
}
