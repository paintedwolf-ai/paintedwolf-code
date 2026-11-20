//go:build darwin

package configlayout

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <stdlib.h>
#include "private_guest_seal_darwin.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"unsafe"
)

func verifyPrivateGuestSeal(app, helper string, manifest []byte) error {
	if len(manifest) == 0 {
		return errors.New("private service manifest is empty")
	}
	outer, exe := C.CString(app), C.CString(helper)
	defer C.free(unsafe.Pointer(outer))
	defer C.free(unsafe.Pointer(exe))
	status := C.pw_verify_guest_runtime(outer, exe, (*C.UInt8)(unsafe.Pointer(&manifest[0])), C.CFIndex(len(manifest)))
	if status != 0 {
		return fmt.Errorf("private service package seal (OSStatus %d)", status)
	}
	return nil
}
