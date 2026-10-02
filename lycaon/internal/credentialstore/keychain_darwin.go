package credentialstore

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation -framework Foundation -framework LocalAuthentication
#include <stdlib.h>
#include "keychain_darwin.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

type keychainStatus int32

const (
	keychainNotFound           keychainStatus = -25300
	keychainDuplicate          keychainStatus = -25299
	keychainMissingEntitlement keychainStatus = -34018
	// Read-back rejections from keychain_darwin.m.
	keychainUnexpectedAccessibility   keychainStatus = C.PW_KEYCHAIN_UNEXPECTED_ACCESSIBILITY
	keychainUnexpectedSynchronization keychainStatus = C.PW_KEYCHAIN_UNEXPECTED_SYNCHRONIZATION
)

func (s keychainStatus) Error() string {
	switch s {
	case keychainMissingEntitlement:
		return "macOS data protection Keychain requires the provisioned, signed host bundle (OSStatus -34018)"
	case keychainUnexpectedAccessibility:
		return "macOS data protection Keychain item is not limited to this device while unlocked"
	case keychainUnexpectedSynchronization:
		return "macOS data protection Keychain item is synchronizable"
	default:
		return fmt.Sprintf("macOS data protection Keychain failed (OSStatus %d)", int32(s))
	}
}

type dataProtectionItems struct{}

func (dataProtectionItems) read(account string) ([]byte, error) {
	service, name := C.CString(keychainService), C.CString(account)
	defer C.free(unsafe.Pointer(service))
	defer C.free(unsafe.Pointer(name))
	var data *C.uchar
	var size C.long
	status := C.pw_keychain_read(service, name, &data, &size) //nolint:gocritic // dupSubExpr flags cgo's generated output-pointer nil check.
	if status != 0 {
		return nil, keychainStatus(status)
	}
	defer C.pw_keychain_free(data, size)
	return C.GoBytes(unsafe.Pointer(data), C.int(size)), nil
}

func (dataProtectionItems) create(account string, value []byte) error {
	service, name := C.CString(keychainService), C.CString(account)
	defer C.free(unsafe.Pointer(service))
	defer C.free(unsafe.Pointer(name))
	status := C.pw_keychain_create(service, name, (*C.uchar)(unsafe.Pointer(unsafe.SliceData(value))), C.long(len(value)))
	if status != 0 {
		return keychainStatus(status)
	}
	return nil
}

func (dataProtectionItems) remove(account string) error {
	service, name := C.CString(keychainService), C.CString(account)
	defer C.free(unsafe.Pointer(service))
	defer C.free(unsafe.Pointer(name))
	if status := C.pw_keychain_remove(service, name); status != 0 {
		return keychainStatus(status)
	}
	return nil
}
