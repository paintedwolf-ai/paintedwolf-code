//go:build darwin || linux

package backup

import "golang.org/x/sys/unix"

func availableUpgradeBytes(path string) (uint64, bool, error) {
	var state unix.Statfs_t
	if err := unix.Statfs(path, &state); err != nil {
		return 0, false, err
	}
	return state.Bavail * uint64(state.Bsize), true, nil
}
