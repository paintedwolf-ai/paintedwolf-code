//go:build !darwin && !linux && !windows

package db

func rebuildAvailableBytes(string) (uint64, bool, error) { return 0, false, nil }
