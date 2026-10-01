//go:build linux

package workspace

import (
	"io/fs"
	"syscall"
)

func sourceFileIdentity(info fs.FileInfo) fileIdentity {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fileIdentity{}
	}
	return fileIdentity{
		Inode:        stat.Ino,
		ChangeSecond: stat.Ctim.Sec,
		ChangeNano:   stat.Ctim.Nsec,
	}
}
