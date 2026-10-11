//go:build windows

package desktoptrash

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	ole32              = windows.NewLazySystemDLL("ole32.dll")
	coInitialize       = ole32.NewProc("CoInitializeEx")
	coUninitialize     = ole32.NewProc("CoUninitialize")
	coCreate           = ole32.NewProc("CoCreateInstance")
	coFree             = ole32.NewProc("CoTaskMemFree")
	shellCreate        = windows.NewLazySystemDLL("shell32.dll").NewProc("SHCreateItemFromParsingName")
	fileOperationClass = windows.GUID{Data1: 0x3ad05575, Data2: 0x8857, Data3: 0x4850, Data4: [8]byte{0x92, 0x77, 0x11, 0xb8, 0x5b, 0xdb, 0x8e, 0x09}}
	fileOperationIID   = windows.GUID{Data1: 0x947aab5f, Data2: 0x0a5c, Data3: 0x4c13, Data4: [8]byte{0xb4, 0xd6, 0x4b, 0xf7, 0x83, 0x6f, 0xc9, 0xf8}}
	shellItemIID       = windows.GUID{Data1: 0x43826d1e, Data2: 0xe718, Data3: 0x42ee, Data4: [8]byte{0xbc, 0x55, 0xa1, 0xe2, 0x61, 0xc3, 0x7b, 0xfe}}
	sinkIID            = windows.GUID{Data1: 0x04b0f1a7, Data2: 0x9490, Data3: 0x44bc, Data4: [8]byte{0x96, 0xe1, 0x42, 0x96, 0xa3, 0x12, 0x52, 0xe2}}
	unknownIID         = windows.GUID{Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
)

type shellInterface struct{ table *[23]uintptr }

func (object *shellInterface) call(index int, args ...uintptr) error {
	values := append([]uintptr{uintptr(unsafe.Pointer(object))}, args...)
	result, _, _ := syscall.SyscallN(object.table[index], values...)
	return shellResult(result)
}
func (object *shellInterface) release() {
	_, _, _ = syscall.SyscallN(object.table[2], uintptr(unsafe.Pointer(object)))
}
func shellResult(result uintptr) error {
	if int32(result) < 0 {
		return fmt.Errorf("native recycle operation failed: HRESULT 0x%08x", uint32(result))
	}
	return nil
}

func platformMove(path string) (Receipt, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	result, _, _ := coInitialize.Call(0, 2|4)
	if err := shellResult(result); err != nil {
		return Receipt{}, err
	}
	defer func() { _, _, _ = coUninitialize.Call() }()
	var operation *shellInterface
	result, _, _ = coCreate.Call(uintptr(unsafe.Pointer(&fileOperationClass)), 0, 1, uintptr(unsafe.Pointer(&fileOperationIID)), uintptr(unsafe.Pointer(&operation)))
	if err := shellResult(result); err != nil {
		return Receipt{}, err
	}
	var sink *recycleSink
	defer func() { operation.release(); runtime.KeepAlive(sink) }()
	// RECYCLEONDELETE requests recycling explicitly; errors stop instead of continuing silently.
	if err := operation.call(5, 0x00080000|0x00100000|0x0400|0x0010|0x0004|0x2000); err != nil {
		return Receipt{}, err
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return Receipt{}, err
	}
	var item *shellInterface
	result, _, _ = shellCreate.Call(uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&shellItemIID)), uintptr(unsafe.Pointer(&item)))
	if err := shellResult(result); err != nil {
		return Receipt{}, err
	}
	defer item.release()
	sink = newRecycleSink()
	if err := operation.call(18, uintptr(unsafe.Pointer(item)), uintptr(unsafe.Pointer(sink))); err != nil {
		return Receipt{}, err
	}
	err = operation.call(21)
	runtime.KeepAlive(sink)
	if err != nil {
		return Receipt{Path: sink.path}, err
	}
	var aborted int32
	if err := operation.call(22, uintptr(unsafe.Pointer(&aborted))); err != nil {
		return Receipt{Path: sink.path}, err
	}
	if aborted != 0 {
		return Receipt{Path: sink.path}, fmt.Errorf("native recycling was canceled")
	}
	if sink.err != nil {
		return Receipt{}, sink.err
	}
	if sink.path == "" {
		return Receipt{}, fmt.Errorf("native recycling returned no recovery location")
	}
	return Receipt{Path: sink.path}, nil
}

func removeTrashMetadata(receipt Receipt) error {
	name := filepath.Base(receipt.Path)
	if !strings.HasPrefix(name, "$R") {
		return nil
	}
	err := os.Remove(filepath.Join(filepath.Dir(receipt.Path), "$I"+name[2:]))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
