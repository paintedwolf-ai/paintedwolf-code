//go:build linux

package layeredfs

import (
	"golang.org/x/sys/unix"
)

const (
	fsCasefoldFlag  = 0x40000000 // FS_CASEFOLD_FL
	msdosSuperMagic = 0x4d44
	exfatSuperMagic = 0x2011bab0
)

// ProbeNameFolding reads how the filesystem holding dir compares names: a casefold directory folds
// case and Unicode forms, and the FAT family folds case.
func ProbeNameFolding(dir string) NameFolding {
	var folding NameFolding
	if fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0); err == nil {
		if flags, err := unix.IoctlGetUint32(fd, unix.FS_IOC_GETFLAGS); err == nil && flags&fsCasefoldFlag != 0 {
			folding = NameFolding{Case: true, Unicode: true}
		}
		_ = unix.Close(fd)
	}
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err == nil {
		switch uint32(st.Type) { //nolint:gosec // Filesystem magic numbers are 32-bit values.
		case msdosSuperMagic, exfatSuperMagic:
			folding.Case = true
		}
	}
	return folding
}
