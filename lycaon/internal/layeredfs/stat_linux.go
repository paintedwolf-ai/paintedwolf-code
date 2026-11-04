//go:build linux

package layeredfs

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func statMtime(st unix.Stat_t) (int64, int64) {
	return int64(st.Mtim.Sec), int64(st.Mtim.Nsec)
}

func statCtime(st unix.Stat_t) (int64, int64) {
	return int64(st.Ctim.Sec), int64(st.Ctim.Nsec)
}

func readSymlinkXattrAt(parentFD int, name string, stBefore unix.Stat_t) (map[string][]byte, error) {
	procPath := fmt.Sprintf("/proc/self/fd/%d/%s", parentFD, name)
	out, err := listXattrSymlinkProcPath(procPath)
	if err != nil {
		return nil, err
	}
	var stAfter unix.Stat_t
	if err := unix.Fstatat(parentFD, name, &stAfter, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, err
	}
	if stAfter.Dev != stBefore.Dev || stAfter.Ino != stBefore.Ino || (stAfter.Mode&unix.S_IFMT) != unix.S_IFLNK {
		return nil, ErrSourceChangedDuringCapture
	}
	return out, nil
}

func statfsBlockSize(st *unix.Statfs_t) uint64 { return uint64(st.Bsize) }
