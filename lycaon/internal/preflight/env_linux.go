//go:build linux

package preflight

import (
	"golang.org/x/sys/unix"
)

// Linux has no OS floor, so the version probe has nothing to compare.
var osProductVersion func() (string, error)

// freeBytes reports space available to an unprivileged user on path's volume.
func freeBytes(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	size := st.Bsize
	if size <= 0 {
		return 0, nil
	}
	return st.Bavail * uint64(size), nil
}
