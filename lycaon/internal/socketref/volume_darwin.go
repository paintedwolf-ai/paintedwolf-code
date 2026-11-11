//go:build darwin && cgo

package socketref

/*
#cgo LDFLAGS: -framework Foundation
#include <stdlib.h>
#include "volume_darwin.h"
*/
import "C"

import (
	"errors"
	"unsafe"
)

// Foundation selects a new temporary directory on the service's own volume.
func volumeTemporaryDirectory(path string) (string, error) {
	input := C.CString(path)
	defer C.free(unsafe.Pointer(input))
	var result, failure *C.char
	status := C.pw_socketref_volume_temporary(input, &result, &failure)
	if result != nil {
		defer C.free(unsafe.Pointer(result))
	}
	if failure != nil {
		defer C.free(unsafe.Pointer(failure))
	}
	if status != 0 || result == nil {
		if failure != nil {
			return "", errors.New(C.GoString(failure))
		}
		return "", ErrUnsupported
	}
	return C.GoString(result), nil
}
