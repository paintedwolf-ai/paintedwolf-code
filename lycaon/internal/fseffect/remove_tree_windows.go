//go:build windows

package fseffect

import (
	"errors"
	"io"
	"os"
	"unsafe"

	"github.com/lycaon/lycaon/internal/fspath"
	"golang.org/x/sys/windows"
)

func RemoveTreeGuarded(loc Location, expected string) error {
	parent, err := openWindowsParent(loc, false, true)
	if err != nil {
		return err
	}
	defer parent.close()
	handle, err := ntOpenLeaf(parent.handle, parent.name, windows.DELETE|windows.FILE_GENERIC_READ)
	if err != nil {
		return err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	identity, err := fspath.EntryIdentityHandle(handle)
	if err != nil {
		return err
	}
	if expected == "" || identity != expected {
		return ErrPostcondition
	}
	if err := removeWindowsTree(handle); err != nil {
		return err
	}
	return flushHandle(parent.handle)
}

func removeWindowsTree(handle windows.Handle) error {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 && info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT == 0 {
		var duplicate windows.Handle
		process := windows.CurrentProcess()
		if err := windows.DuplicateHandle(process, handle, process, &duplicate, 0, false, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return err
		}
		dir := os.NewFile(uintptr(duplicate), "quarantined directory")
		defer func() { _ = dir.Close() }()
		for {
			names, err := dir.Readdirnames(256)
			for _, name := range names {
				child, err := ntOpenLeaf(handle, name, windows.DELETE|windows.FILE_GENERIC_READ)
				if err != nil {
					return err
				}
				removeErr := removeWindowsTree(child)
				_ = windows.CloseHandle(child)
				if removeErr != nil {
					return removeErr
				}
			}
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return err
			}
		}
	}
	flags := uint32(windows.FILE_DISPOSITION_DELETE | windows.FILE_DISPOSITION_POSIX_SEMANTICS | windows.FILE_DISPOSITION_IGNORE_READONLY_ATTRIBUTE)
	var iosb windows.IO_STATUS_BLOCK
	err := windows.NtSetInformationFile(handle, &iosb, (*byte)(unsafe.Pointer(&flags)), uint32(unsafe.Sizeof(flags)), windows.FileDispositionInformationEx)
	if status, ok := err.(windows.NTStatus); ok {
		return status.Errno()
	}
	return err
}
