//go:build windows

package hostresources

import (
	"errors"
	"unsafe"

	"golang.org/x/sys/windows"
)

var waitNamedPipeW = windows.NewLazySystemDLL("kernel32.dll").NewProc("WaitNamedPipeW")

func platformNamedPipeAvailable(name string) (bool, error) {
	path, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false, err
	}
	available, _, callErr := waitNamedPipeW.Call(uintptr(unsafe.Pointer(path)), 0)
	if available != 0 {
		return true, nil
	}
	if errors.Is(callErr, windows.ERROR_SEM_TIMEOUT) || errors.Is(callErr, windows.ERROR_PIPE_BUSY) {
		return true, nil
	}
	if errors.Is(callErr, windows.ERROR_FILE_NOT_FOUND) || errors.Is(callErr, windows.ERROR_PATH_NOT_FOUND) {
		return false, nil
	}
	return false, callErr
}
