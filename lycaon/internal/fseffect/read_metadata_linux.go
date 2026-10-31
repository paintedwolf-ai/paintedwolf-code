//go:build linux

package fseffect

import (
	"time"

	"golang.org/x/sys/unix"
)

func metadataOpenFlags(bool) int { return unix.O_PATH }

func birthTime(fd int, name string, follow bool, _ *unix.Stat_t) time.Time {
	flags := unix.AT_STATX_SYNC_AS_STAT
	if name == "" {
		flags |= unix.AT_EMPTY_PATH
	} else if !follow {
		flags |= unix.AT_SYMLINK_NOFOLLOW
	}
	var sx unix.Statx_t
	if err := unix.Statx(fd, name, flags, unix.STATX_BTIME, &sx); err != nil {
		return time.Time{}
	}
	if sx.Mask&unix.STATX_BTIME != 0 && (sx.Btime.Sec > 0 || sx.Btime.Nsec > 0) {
		return time.Unix(sx.Btime.Sec, int64(sx.Btime.Nsec)).UTC()
	}
	return time.Time{}
}
