//go:build darwin || linux

package workspace

import "golang.org/x/sys/unix"

func availableStorageBytes(path string) (uint64, bool, error) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, false, err
	}
	size := stat.Bsize
	if size <= 0 {
		return 0, false, nil
	}
	return stat.Bavail * uint64(size), true, nil
}
