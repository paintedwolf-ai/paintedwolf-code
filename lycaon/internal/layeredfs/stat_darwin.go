//go:build darwin

package layeredfs

import "golang.org/x/sys/unix"

func statMtime(st unix.Stat_t) (int64, int64) {
	return st.Mtim.Sec, st.Mtim.Nsec
}

func statCtime(st unix.Stat_t) (int64, int64) {
	return st.Ctim.Sec, st.Ctim.Nsec
}

func readSymlinkXattrAt(parentFD int, name string, stBefore unix.Stat_t) (map[string][]byte, error) {
	fd, err := unix.Openat(parentFD, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_SYMLINK, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	var stAfter unix.Stat_t
	if err := unix.Fstat(fd, &stAfter); err != nil {
		return nil, err
	}
	if stAfter.Dev != stBefore.Dev || stAfter.Ino != stBefore.Ino || (stAfter.Mode&unix.S_IFMT) != unix.S_IFLNK {
		return nil, ErrSourceChangedDuringCapture
	}
	return listXattrFD(fd)
}
