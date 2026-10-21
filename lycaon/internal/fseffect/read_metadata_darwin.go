//go:build darwin

package fseffect

import (
	"os"
	"time"

	"golang.org/x/sys/unix"
)

func metadataOpenFlags(directory bool) int {
	if directory {
		return unix.O_SEARCH
	}
	return unix.O_EXEC
}

func birthTime(_ *os.File, _ string, _ bool, st *unix.Stat_t) time.Time {
	if st.Btim.Sec > 0 || (st.Btim.Sec == 0 && st.Btim.Nsec > 0) {
		return time.Unix(int64(st.Btim.Sec), int64(st.Btim.Nsec)).UTC()
	}
	return time.Time{}
}
