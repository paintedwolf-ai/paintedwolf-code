//go:build darwin || linux

package fspath

import (
	"io/fs"
	"math"
	"syscall"
)

func EntryAllocation(info fs.FileInfo) Allocation {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Blocks < 0 || stat.Blocks > math.MaxInt64/512 {
		return Allocation{}
	}
	return Allocation{Identity: AllocationIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}, Bytes: stat.Blocks * 512, Links: uint64(stat.Nlink), Known: true}
}
