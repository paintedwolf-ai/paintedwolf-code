//go:build !darwin && !linux

package workspace

func availableStorageBytes(string) (uint64, bool, error) {
	return 0, false, nil
}
