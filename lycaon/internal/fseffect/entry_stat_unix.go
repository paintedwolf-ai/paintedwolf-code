//go:build darwin || linux

package fseffect

import (
	"io/fs"
	"os"
	"path/filepath"
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
		if err := unix.Fstat(int(r.root.file.Fd()), &st); err != nil {
			return EntryStat{}, err
		}
		if admit != nil {
			if err := admit(r.root.path); err != nil {
				return EntryStat{}, err
			}
		}
		out := entryStat(filepath.Base(r.root.path), &st)
		out.BirthTime = birthTime(r.root.file, "", true, &st)
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
		out.BirthTime = birthTime(dir, name, follow, st)
		return nil, nil
	})
	return out, err
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
