//go:build linux

package preflight

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// osComponent names the compared system version in floor verdicts.
const osComponent = "kernel"

// osProductVersion reports the running kernel release as a bare dotted version.
var osProductVersion = func() (string, error) {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "", err
	}
	return kernelRelease(unix.ByteSliceToString(u.Release[:]))
}

// libcPaths lists where distributions install the C library, most common first.
var libcPaths = []string{
	"/lib64/libc.so.6",
	"/lib/x86_64-linux-gnu/libc.so.6",
	"/lib/aarch64-linux-gnu/libc.so.6",
	"/usr/lib64/libc.so.6",
	"/usr/lib/x86_64-linux-gnu/libc.so.6",
	"/usr/lib/aarch64-linux-gnu/libc.so.6",
	"/usr/lib/libc.so.6",
	"/lib/libc.so.6",
}

var errLibcNotFound = errors.New("preflight: libc.so.6 not found at any conventional path")

// libcVersion reads the glibc version from the first installed C library.
var libcVersion = func() (string, error) {
	for _, path := range libcPaths {
		if _, err := os.Stat(path); err == nil {
			return glibcVersionFromELF(path)
		}
	}
	return "", errLibcNotFound
}

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
