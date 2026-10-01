//go:build darwin

package sourcecatalog

import (
	"os"
	"syscall"
)

func directoryStampOf(info os.FileInfo) DirectoryStamp {
	stamp := DirectoryStamp{Modified: info.ModTime().UnixNano()}
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		stamp.Changed = stat.Ctimespec.Sec*1e9 + stat.Ctimespec.Nsec
	}
	return stamp
}
