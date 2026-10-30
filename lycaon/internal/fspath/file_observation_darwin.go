//go:build darwin

package fspath

import (
	"io/fs"
	"syscall"
)

func ObserveFile(info fs.FileInfo) FileObservation {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return FileObservation{}
	}
	return FileObservation{Identity: AllocationIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}, Changed: stat.Ctimespec.Sec*1e9 + stat.Ctimespec.Nsec, Links: uint64(stat.Nlink), Known: true}
}
