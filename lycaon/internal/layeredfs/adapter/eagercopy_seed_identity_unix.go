//go:build darwin || linux

package adapter

import (
	"io/fs"
	"syscall"
)

// sharedFileID is a file's identity when the file has more than one name.
func sharedFileID(info fs.FileInfo) (seedFileID, bool) {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Nlink < 2 {
		return seedFileID{}, false
	}
	return seedFileID{dev: uint64(st.Dev), ino: st.Ino}, true
}
