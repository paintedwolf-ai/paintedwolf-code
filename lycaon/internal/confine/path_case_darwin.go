//go:build darwin

package confine

import (
	"golang.org/x/sys/unix"
)

// pcCaseSensitive is _PC_CASE_SENSITIVE from <sys/unistd.h>.
const pcCaseSensitive = 11

// volumeFoldsCase asks the volume's mount root, never dir itself: listing a
// TCC-protected folder such as ~/Documents raises a privacy prompt.
func volumeFoldsCase(dir string) bool {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return true
	}
	mount := unix.ByteSliceToString(st.Mntonname[:])
	if mount == "" {
		return true
	}
	sensitive, err := unix.Pathconf(mount, pcCaseSensitive)
	if err != nil {
		return true
	}
	return sensitive == 0
}
