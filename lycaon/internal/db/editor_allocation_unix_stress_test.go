//go:build stress && unix

package db_test

import (
	"io/fs"
	"syscall"
)

func yearAllocatedBytes(info fs.FileInfo) int64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return stat.Blocks * 512
	}
	return info.Size()
}
