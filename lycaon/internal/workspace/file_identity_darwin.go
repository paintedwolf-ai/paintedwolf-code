//go:build darwin

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
		ChangeSecond: stat.Ctimespec.Sec,
		ChangeNano:   stat.Ctimespec.Nsec,
	}
}
