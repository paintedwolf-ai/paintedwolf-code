//go:build darwin || linux

package workspacebaseline

import (
	"time"

	"golang.org/x/sys/unix"
)

// lchtimes sets a symlink's own modification time without following it.
func lchtimes(path string, stamp time.Time) error {
	ts := unix.NsecToTimeval(stamp.UnixNano())
	return unix.Lutimes(path, []unix.Timeval{ts, ts})
}
