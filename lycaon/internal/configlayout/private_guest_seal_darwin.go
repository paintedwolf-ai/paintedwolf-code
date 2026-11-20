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
	"os"
	"unsafe"
)

func verifyPrivateGuestSeal(app, helper string, manifest, cdhash []byte) error {
	executable, err := os.ReadFile(helper)
	if err != nil {
		return err
	}
	outer, exe := C.CString(app), C.CString(helper)
	defer C.free(unsafe.Pointer(outer))
	defer C.free(unsafe.Pointer(exe))
	var seal C.SecStaticCodeRef
	status := C.pw_verify_guest_runtime(outer, exe, &seal)
	if status != 0 {
		return fmt.Errorf("private service package seal (OSStatus %d)", status)
	}
	defer C.CFRelease(C.CFTypeRef(seal))
	if err := verifyGuestResource(seal, "Resources/service-guest/manifest.json", manifest); err != nil {
		return err
	}
	if err := verifyGuestResource(seal, "Resources/service-guest/pw-service-vm.cdhash", cdhash); err != nil {
		return err
	}
	return verifyGuestResource(seal, "Resources/service-guest/pw-service-vm", executable)
}

func verifyGuestResource(seal C.SecStaticCodeRef, path string, data []byte) error {
	if len(data) == 0 {
		return errors.New("empty private service resource")
	}
	resource := C.CString(path)
	defer C.free(unsafe.Pointer(resource))
	status := C.pw_guest_resource(seal, resource, (*C.UInt8)(unsafe.Pointer(&data[0])), C.CFIndex(len(data)))
	if status != 0 {
		return fmt.Errorf("private service resource %s seal (OSStatus %d)", path, status)
	}
	return nil
}

func openGuestResourceEnvelope(app string) (C.SecStaticCodeRef, func(), error) {
	path := C.CString(app)
	defer C.free(unsafe.Pointer(path))
	var seal C.SecStaticCodeRef
	status := C.pw_open_resource_envelope(path, &seal)
	if status != 0 {
		return 0, nil, fmt.Errorf("open package resource envelope (OSStatus %d)", status)
	}
	return seal, func() { C.CFRelease(C.CFTypeRef(seal)) }, nil
}
