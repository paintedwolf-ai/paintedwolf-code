package fseffect

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func SyncDirectory(root *os.Root, path string) error {
	if filepath.Clean(path) == "." {
		name, err := windows.UTF16PtrFromString(root.Name())
		if err != nil {
			return err
		}
		handle, err := windows.CreateFile(name, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, windowsShare, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
		if err != nil {
			return err
		}
		defer func() { _ = windows.CloseHandle(handle) }()
		return windows.FlushFileBuffers(handle)
	}
	parent, err := openWindowsParent(Location{Root: root.Name(), Rel: path}, false, true)
	if err != nil {
		return err
	}
	defer parent.close()
	handle, err := ntOpenRelative(parent.handle, parent.name, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	return windows.FlushFileBuffers(handle)
}
