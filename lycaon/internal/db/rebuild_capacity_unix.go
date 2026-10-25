//go:build darwin || linux

package db

import "golang.org/x/sys/unix"

func rebuildAvailableBytes(path string) (uint64, bool, error) {
	var state unix.Statfs_t
	if err := unix.Statfs(path, &state); err != nil {
		return 0, false, err
	}
	if state.Bsize <= 0 {
		return 0, false, nil
	}
	return state.Bavail * uint64(state.Bsize), true, nil
}
