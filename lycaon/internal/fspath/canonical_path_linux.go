package fspath

import (
	"path/filepath"

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
// component exists and none is a link. Most checks take this route, and resolving
// through the general walk costs several allocations per component.
func linkFreePath(path string) bool {
	var st unix.Stat_t
	for end := 1; end <= len(path); end++ {
		if end < len(path) && path[end] != filepath.Separator {
			continue
		}
		if unix.Lstat(path[:end], &st) != nil {
			return false
		}
		kind := st.Mode & unix.S_IFMT
		if kind == unix.S_IFLNK || (end < len(path) && kind != unix.S_IFDIR) {
			return false
		}
	}
	return true
}
