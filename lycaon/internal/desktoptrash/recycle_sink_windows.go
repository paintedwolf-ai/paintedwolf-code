//go:build windows

package desktoptrash

import (
	"fmt"
	"golang.org/x/sys/windows"
	"syscall"
	"unsafe"
)

// IFileOperation retains this sink only during synchronous PerformOperations.
type recycleSink struct {
	table *[19]uintptr
	path  string
	err   error
}

var recycleCallbacks = [19]uintptr{
	syscall.NewCallback(func(self, iid, result uintptr) uintptr {
		want := *(*windows.GUID)(unsafe.Pointer(iid))
		if want != sinkIID && want != unknownIID {
			*(*uintptr)(unsafe.Pointer(result)) = 0
			return 0x80004002
		}
		*(*uintptr)(unsafe.Pointer(result)) = self
		return 0
	}),
	syscall.NewCallback(func(uintptr) uintptr { return 1 }),
	syscall.NewCallback(func(uintptr) uintptr { return 1 }),
	syscall.NewCallback(func(uintptr) uintptr { return 0 }),
	syscall.NewCallback(func(uintptr, uintptr) uintptr { return 0 }),
	syscall.NewCallback(func(uintptr, uintptr, uintptr, uintptr) uintptr { return 0 }),
	syscall.NewCallback(func(uintptr, uintptr, uintptr, uintptr, uintptr, uintptr) uintptr { return 0 }),
	syscall.NewCallback(func(uintptr, uintptr, uintptr, uintptr, uintptr) uintptr { return 0 }),
	syscall.NewCallback(func(uintptr, uintptr, uintptr, uintptr, uintptr, uintptr, uintptr) uintptr { return 0 }),
	syscall.NewCallback(func(uintptr, uintptr, uintptr, uintptr, uintptr) uintptr { return 0 }),
	syscall.NewCallback(func(uintptr, uintptr, uintptr, uintptr, uintptr, uintptr, uintptr) uintptr { return 0 }),
	syscall.NewCallback(func(_ uintptr, flags uintptr, _ uintptr) uintptr {
		// A permanent-delete fallback must be vetoed before it touches the entry.
		if flags&0x80 == 0 {
			return 0x80004004
		}
		return 0
	}),
	syscall.NewCallback(recycleCompleted),
	syscall.NewCallback(func(uintptr, uintptr, uintptr, uintptr) uintptr { return 0 }),
	syscall.NewCallback(func(uintptr, uintptr, uintptr, uintptr, uintptr, uintptr, uintptr, uintptr) uintptr { return 0 }),
	syscall.NewCallback(func(uintptr, uintptr, uintptr) uintptr { return 0 }),
	syscall.NewCallback(func(uintptr) uintptr { return 0 }),
	syscall.NewCallback(func(uintptr) uintptr { return 0 }),
	syscall.NewCallback(func(uintptr) uintptr { return 0 }),
}

func newRecycleSink() *recycleSink { return &recycleSink{table: &recycleCallbacks} }
func recycleCompleted(self, _flags, _item, result, recycled uintptr) uintptr {
	sink := (*recycleSink)(unsafe.Pointer(self))
	sink.err = shellResult(result)
	if sink.err != nil {
		return 0
	}
	if recycled == 0 {
		sink.err = fmt.Errorf("native recycling returned no retained item")
		return 0
	}
	item := (*shellInterface)(unsafe.Pointer(recycled))
	var path *uint16
	sink.err = item.call(5, 0x80058000, uintptr(unsafe.Pointer(&path)))
	if sink.err == nil && path != nil {
		sink.path = windows.UTF16PtrToString(path)
		_, _, _ = coFree.Call(uintptr(unsafe.Pointer(path)))
	}
	return 0
}
