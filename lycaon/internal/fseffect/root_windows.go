//go:build windows

package fseffect

import (
	"golang.org/x/sys/windows"
	"unsafe"
)

func openWindowsRoot(path string, write bool) (windows.Handle, error) {
	access := uint32(windows.FILE_GENERIC_READ)
	if write {
		access |= windows.FILE_GENERIC_WRITE
	}
	return openWindowsRootAccess(path, access)
}

func openWindowsRootAccess(path string, access uint32) (windows.Handle, error) {
	objectName, err := windows.NewNTUnicodeString(windowsNTPath(path))
	if err != nil {
		return 0, err
	}
	oa := windows.OBJECT_ATTRIBUTES{
		Length:     uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		ObjectName: objectName,
		Attributes: windows.OBJ_CASE_INSENSITIVE | windows.OBJ_DONT_REPARSE,
	}
	return ntCreate(&oa, access, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE)
}
