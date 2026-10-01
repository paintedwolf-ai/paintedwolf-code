//go:build darwin || linux

package workspace

import (
	"io/fs"
	"syscall"
)

func allocatedFileBytes(info fs.FileInfo) int64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return info.Size()
	}
	return stat.Blocks * 512
}
