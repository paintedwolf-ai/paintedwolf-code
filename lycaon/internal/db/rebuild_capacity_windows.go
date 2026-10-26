//go:build windows

package db

import "golang.org/x/sys/windows"

func rebuildAvailableBytes(path string) (uint64, bool, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, false, err
	}
	var available, total, free uint64
	if err := windows.GetDiskFreeSpaceEx(name, &available, &total, &free); err != nil {
		return 0, false, err
	}
	return available, true, nil
}
