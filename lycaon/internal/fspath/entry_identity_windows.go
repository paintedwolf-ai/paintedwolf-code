//go:build windows

package fspath

import (
	"fmt"

	"golang.org/x/sys/windows"
)

// EntryIdentity identifies the selected entry, including a reparse point itself.
func EntryIdentity(path string) (string, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return "", err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	return EntryIdentityHandle(handle)
}

func EntryIdentityHandle(handle windows.Handle) (string, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d:%d:%d:%d", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow, info.CreationTime.HighDateTime, info.CreationTime.LowDateTime), nil
}

func SameFilesystem(from, to string) (bool, error) {
	volume := func(path string) (uint32, error) {
		name, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return 0, err
		}
		handle, err := windows.CreateFile(name, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		if err != nil {
			return 0, err
		}
		defer func() { _ = windows.CloseHandle(handle) }()
		var info windows.ByHandleFileInformation
		if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
			return 0, err
		}
		return info.VolumeSerialNumber, nil
	}
	a, err := volume(from)
	if err != nil {
		return false, err
	}
	b, err := volume(to)
	return a == b, err
}
