//go:build darwin

package layeredfs

import "golang.org/x/sys/unix"

func statMtime(st unix.Stat_t) (int64, int64) {
	return st.Mtim.Sec, st.Mtim.Nsec
}

func statCtime(st unix.Stat_t) (int64, int64) {
	return st.Ctim.Sec, st.Ctim.Nsec
}
