//go:build darwin

package fseffect

import (
	"golang.org/x/sys/unix"
	"os"
	"syscall"
	"time"
)

func renameNoReplace(from int, name string, to int, dest string) error {
	return unix.RenameatxNp(from, name, to, dest, unix.RENAME_EXCL)
}

// utimeOmit leaves one timestamp unchanged in utimensat (sys/stat.h).
const utimeOmit = -2

func platformAccessTime(info os.FileInfo) (time.Time, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(st.Atimespec.Sec, st.Atimespec.Nsec), true
}
