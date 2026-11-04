//go:build linux

package adapter

import (
	"io/fs"
	"syscall"
)

func statIdentityOf(info fs.FileInfo) statIdentity {
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return statIdentity{}
	}
	return statIdentity{ctimeNS: st.Ctim.Sec*1e9 + st.Ctim.Nsec, inode: st.Ino}
}
