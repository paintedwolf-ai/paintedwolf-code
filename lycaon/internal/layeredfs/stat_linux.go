//go:build linux

package layeredfs

import "golang.org/x/sys/unix"

func statMtime(st unix.Stat_t) (int64, int64) {
	return int64(st.Mtim.Sec), int64(st.Mtim.Nsec)
}

func statCtime(st unix.Stat_t) (int64, int64) {
	return int64(st.Ctim.Sec), int64(st.Ctim.Nsec)
}
