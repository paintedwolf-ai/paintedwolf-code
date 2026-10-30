//go:build darwin || linux

package fseffect

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Stat reports an entry's metadata through the jailed read walk. Without
// follow it describes the final entry itself, including a dangling link;
// with follow, contained links resolve and outward links refuse. admit sees
// the native entry path before any metadata is returned.
func (r *ReadRoot) Stat(rel string, follow bool, admit func(string) error) (EntryStat, error) {
	relative, err := exactReadRelative(rel)
	if err != nil {
		return EntryStat{}, err
	}
	if relative == "." || relative == "" {
		var st unix.Stat_t
		if err := unix.Fstat(r.root.fd, &st); err != nil {
			return EntryStat{}, err
		}
		if admit != nil {
			if err := admit(r.root.path); err != nil {
				return EntryStat{}, err
			}
		}
		out := entryStat(filepath.Base(r.root.path), &st)
		out.BirthTime = birthTime(r.root.fd, "", true, &st)
		return out, nil
	}
	var out EntryStat
	_, err = walkReadContained(r.root, relative, follow, func(dir *os.File, name string, st *unix.Stat_t) (*os.File, error) {
		if admit != nil {
			if err := admit(filepath.Join(dir.Name(), name)); err != nil {
				return nil, err
			}
		}
		out = entryStat(name, st)
		out.BirthTime = birthTime(int(dir.Fd()), name, follow, st)
		return nil, nil
	})
	return out, err
}

// StatChildAt reports an immediate child's no-follow metadata through a held
// contained directory descriptor.
func (r *ReadRoot) StatChildAt(dir *os.File, _ string, name string, admit func(string) error) (EntryStat, error) {
	dirName, err := r.DirectoryPath(dir)
	if err != nil {
		return EntryStat{}, err
	}
	var st unix.Stat_t
	if err := unix.Fstatat(int(dir.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return EntryStat{}, &fs.PathError{Op: "open effect path", Path: name, Err: err}
	}
	if admit != nil {
		actual := dirName + string(filepath.Separator) + name
		if dirName == "" || name == "" || name == "." || name == ".." || strings.ContainsRune(name, filepath.Separator) || strings.HasSuffix(dirName, string(filepath.Separator)) {
			actual = filepath.Join(dirName, name)
		}
		if err := admit(actual); err != nil {
			return EntryStat{}, err
		}
	}
	return entryStat(name, &st), nil
}

func entryStat(name string, st *unix.Stat_t) EntryStat {
	mode := fs.FileMode(st.Mode & 0o777)
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		mode |= fs.ModeDir
	case unix.S_IFLNK:
		mode |= fs.ModeSymlink
	case unix.S_IFIFO:
		mode |= fs.ModeNamedPipe
	case unix.S_IFSOCK:
		mode |= fs.ModeSocket
	case unix.S_IFCHR:
		mode |= fs.ModeDevice | fs.ModeCharDevice
	case unix.S_IFBLK:
		mode |= fs.ModeDevice
	}
	if st.Mode&unix.S_ISUID != 0 {
		mode |= fs.ModeSetuid
	}
	if st.Mode&unix.S_ISGID != 0 {
		mode |= fs.ModeSetgid
	}
	if st.Mode&unix.S_ISVTX != 0 {
		mode |= fs.ModeSticky
	}
	return EntryStat{
		Name: name, Mode: mode, Size: st.Size, ModTime: time.Unix(int64(st.Mtim.Sec), int64(st.Mtim.Nsec)).UTC(),
		POSIX: true, UID: st.Uid, GID: st.Gid, Inode: uint64(st.Ino), Links: uint64(st.Nlink), Device: uint64(st.Dev),
		DeviceMajor: unix.Major(uint64(st.Dev)), DeviceMinor: unix.Minor(uint64(st.Dev)),
		Blocks: int64(st.Blocks), BlockSize: int64(st.Blksize),
		AccessTime: time.Unix(int64(st.Atim.Sec), int64(st.Atim.Nsec)).UTC(), ChangeTime: time.Unix(int64(st.Ctim.Sec), int64(st.Ctim.Nsec)).UTC(),
	}
}
