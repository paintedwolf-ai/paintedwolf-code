//go:build linux

package fseffect

import (
	"golang.org/x/sys/unix"
	"os"
	"syscall"
	"time"
)

func renameNoReplace(from int, name string, to int, dest string) error {
	return unix.Renameat2(from, name, to, dest, unix.RENAME_NOREPLACE)
}

// utimeOmit leaves one timestamp unchanged in utimensat.
const utimeOmit = unix.UTIME_OMIT

func platformAccessTime(info os.FileInfo) (time.Time, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(st.Atim.Sec), int64(st.Atim.Nsec)), true
}
