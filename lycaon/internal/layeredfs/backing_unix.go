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
	if err := unix.Renameat(dirFD, oldName, dirFD, newName); err != nil {
		return err
	}
	return syncFD(dirFD)
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

func listXattrPath(path string) (map[string][]byte, error) {
	size, err := unix.Llistxattr(path, nil)
	if ignorableXattrErr(err) || size <= 0 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	buf := make([]byte, size)
	size, err = unix.Llistxattr(path, buf)
	if ignorableXattrErr(err) || size <= 0 {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make(map[string][]byte)
	for _, raw := range bytes.Split(buf[:size], []byte{0}) {
		if len(raw) == 0 {
			continue
		}
		name := string(raw)
		vSize, gErr := unix.Lgetxattr(path, name, nil)
		if ignorableXattrErr(gErr) || vSize < 0 {
			continue
		}
		if gErr != nil {
			return nil, gErr
		}
		if vSize == 0 {
			out[name] = []byte{}
			continue
		}
		val := make([]byte, vSize)
		vSize, gErr = unix.Lgetxattr(path, name, val)
		if ignorableXattrErr(gErr) {
			continue
		}
		if gErr != nil {
			return nil, gErr
		}
		out[name] = val[:vSize]
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func ignorableXattrErr(err error) bool {
	return err == nil ||
		errors.Is(err, unix.ENOTSUP) ||
		errors.Is(err, unix.EOPNOTSUPP) ||
		errors.Is(err, unix.ENODATA) ||
		errors.Is(err, syscall.Errno(93)) ||
		errors.Is(err, unix.EPERM) ||
		errors.Is(err, unix.EACCES)
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
