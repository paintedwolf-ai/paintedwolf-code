//go:build !darwin && !linux

package db

func auditAvailableBytes(string) (uint64, bool, error) { return 0, false, nil }
