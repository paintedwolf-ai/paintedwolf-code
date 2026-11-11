//go:build darwin || linux

package privategit

import (
	"io/fs"
	"syscall"

	"github.com/lycaon/lycaon/internal/layeredfs"
)

func fillLowerIdentity(le *layeredfs.LowerEntry, info fs.FileInfo) {
	if st, ok := info.Sys().(*syscall.Stat_t); ok && st != nil {
		le.Dev = uint64(st.Dev) //nolint:unconvert // Dev is int32 on darwin
		le.Ino = st.Ino
		le.UID = st.Uid
		le.GID = st.Gid
		le.Nlink = uint32(st.Nlink) //nolint:unconvert,gosec // Nlink is uint16 on darwin and fits 32 bits everywhere.
	}
}
