//go:build darwin || linux

package confine

import (
	"io/fs"
	"syscall"
)

func exclusionHasAliases(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return !ok || stat.Nlink > 1
}
