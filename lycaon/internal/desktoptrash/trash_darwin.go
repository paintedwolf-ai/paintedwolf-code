//go:build darwin && cgo

package desktoptrash

/*
#cgo LDFLAGS: -framework Foundation
#include <stdlib.h>
#include "trash_darwin.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

func platformMove(path string) error {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))

	var cErr *C.char
	res := C.pw_trash_item(cPath, &cErr)
	if res != 0 {
		if cErr != nil {
			defer C.free(unsafe.Pointer(cErr))
			return errors.New(C.GoString(cErr))
		}
		return errors.New("failed to move item to trash")
	}
	return nil
}
