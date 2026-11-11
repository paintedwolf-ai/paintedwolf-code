package fspath

import (
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/unix"
)

func canonicalizeExisting(path string) (string, bool) {
	if linkFreePath(path) {
		return path, true
	}
	resolved, err := filepath.EvalSymlinks(path)
	return resolved, err == nil
}

// linkFreePath reports whether a clean absolute path already names itself: every
// component exists and none is a link. Most checks take this route, and the general
// walk costs several allocations per component, so this one allocates nothing.
func linkFreePath(path string) bool {
	var name [unix.PathMax]byte
	if len(path) >= len(name) {
		return false
	}
	copy(name[:], path)
	workingDirectory := unix.AT_FDCWD
	var st unix.Statx_t
	for end := 1; end <= len(path); end++ {
		if end < len(path) && path[end] != filepath.Separator {
			continue
		}
		name[end] = 0
		// #nosec G103 -- statx reads the NUL-terminated prefix and writes into the caller-owned Statx_t.
		_, _, errno := unix.Syscall6(unix.SYS_STATX, uintptr(workingDirectory), uintptr(unsafe.Pointer(&name[0])),
			unix.AT_SYMLINK_NOFOLLOW, unix.STATX_TYPE, uintptr(unsafe.Pointer(&st)), 0)
		if errno != 0 {
			return false
		}
		kind := uint32(st.Mode) & unix.S_IFMT
		if kind == unix.S_IFLNK || (end < len(path) && kind != unix.S_IFDIR) {
			return false
		}
		if end < len(path) {
			name[end] = filepath.Separator
		}
	}
	return true
}
