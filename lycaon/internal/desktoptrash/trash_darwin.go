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

func platformMove(path string) (Receipt, error) {
	cPath := C.CString(path)
	defer C.free(unsafe.Pointer(cPath))

	var cErr, cResult *C.char
	res := C.pw_trash_item(cPath, &cResult, &cErr)
	if res != 0 {
		if cErr != nil {
			defer C.free(unsafe.Pointer(cErr))
			return Receipt{}, errors.New(C.GoString(cErr))
		}
		return Receipt{}, errors.New("failed to move item to trash")
	}
	if cResult == nil { return Receipt{}, errors.New("trash returned no recovery location") }
 defer C.free(unsafe.Pointer(cResult))
 return Receipt{Path: C.GoString(cResult)}, nil
}
