package fseffect

import (
	"os"

	"golang.org/x/sys/unix"
)

// VolumeOf reads the volume holding file, which may be a metadata-only
// descriptor. Darwin's statfs names the device and mount point itself.
func VolumeOf(file *os.File, _ string) (Volume, error) {
	var st unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &st); err != nil {
		return Volume{}, err
	}
	return Volume{
		Source: unix.ByteSliceToString(st.Mntfromname[:]), Target: unix.ByteSliceToString(st.Mntonname[:]),
		BlockSize: uint64(st.Bsize), Blocks: st.Blocks, Free: st.Bfree, Available: st.Bavail, Files: st.Files, FreeFiles: st.Ffree,
	}, nil
}
