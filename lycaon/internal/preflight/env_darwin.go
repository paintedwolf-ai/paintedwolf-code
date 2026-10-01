//go:build darwin

package preflight

import (
	"golang.org/x/sys/unix"
)

// osProductVersion reads the macOS marketing version from the kernel.
func osProductVersion() (string, error) {
	return unix.Sysctl("kern.osproductversion")
}

// freeBytes reports space available to an unprivileged user on path's volume.
// Bavail (not Bfree) is what the user can actually write.
func freeBytes(path string) (uint64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}
