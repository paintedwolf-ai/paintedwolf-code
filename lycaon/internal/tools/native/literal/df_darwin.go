package literal

import (
	"os"

	"golang.org/x/sys/unix"
)

// statVolume reads the volume holding an open file; Darwin's statfs names
// the mounted device and mount point itself.
func statVolume(file *os.File, _ string) (volume, error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &st); err != nil {
		return volume{}, err
	}
	return volume{
		source: unix.ByteSliceToString(st.Mntfromname[:]), target: unix.ByteSliceToString(st.Mntonname[:]),
		blockSize: uint64(st.Bsize), blocks: st.Blocks, free: st.Bfree, avail: st.Bavail, files: st.Files, freeFiles: st.Ffree,
	}, nil
}
