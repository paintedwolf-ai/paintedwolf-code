//go:build !darwin && !linux

package backup

func availableUpgradeBytes(string) (uint64, bool, error) { return 0, false, nil }
