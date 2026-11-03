//go:build darwin || linux

package literal

import (
	"os"
	"syscall"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

// linkAt makes a symlink carrying the fixture mtime itself.
func linkAt(t *testing.T, target, path string, when time.Time) {
	t.Helper()
	symlink(t, target, path)
	tv := unix.NsecToTimeval(when.UnixNano())
	testutil.FailErr(t, "link time "+path, unix.Lutimes(path, []unix.Timeval{tv, tv}))
}

// ownerIDs reports the numeric owner a fixture entry actually received.
func ownerIDs(t *testing.T, path string) (uint32, uint32) {
	t.Helper()
	info, err := os.Lstat(path)
	testutil.FailErr(t, "lstat "+path, err)
	st := info.Sys().(*syscall.Stat_t)
	return st.Uid, st.Gid
}

// rawStat is the platform's own lstat record for an expectation.
func rawStat(t *testing.T, path string) *unix.Stat_t {
	t.Helper()
	var st unix.Stat_t
	testutil.FailErr(t, "lstat "+path, unix.Lstat(path, &st))
	return &st
}
