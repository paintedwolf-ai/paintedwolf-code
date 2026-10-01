//go:build linux

package sourcecatalog

import (
	"os"
	"syscall"
)

func directoryStampOf(info os.FileInfo) DirectoryStamp {
	stamp := DirectoryStamp{Modified: info.ModTime().UnixNano()}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		stamp.Changed = stat.Ctim.Sec*1e9 + stat.Ctim.Nsec
	}
	return stamp
}
