//go:build windows

package desktoptrash

import (
	"errors"
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	foDelete          = 0x0003
	fofAllowUndo      = 0x0040
	fofNoConfirmation = 0x0010
	fofSilent         = 0x0004
	fofNoErrorUI      = 0x0400
)

type shFileOpStructW struct {
	hwnd                  windows.Handle
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

var (
	shell32              = windows.NewLazySystemDLL("shell32.dll")
	procSHFileOperationW = shell32.NewProc("SHFileOperationW")
)

func platformMove(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	// SHFileOperationW reads a list ended by an extra NUL.
	utf16Chars, err := windows.UTF16FromString(abs)
	if err != nil {
		return err
	}
	doubleNull := append(utf16Chars, 0)

	op := shFileOpStructW{
		wFunc:  foDelete,
		pFrom:  &doubleNull[0],
		fFlags: fofAllowUndo | fofNoConfirmation | fofSilent | fofNoErrorUI,
	}

	ret, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(&op)))
	if ret != 0 {
		return fmt.Errorf("system trash failed with code %d", ret)
	}
	if op.fAnyOperationsAborted != 0 {
		return errors.New("trash operation aborted")
	}
	return nil
}
