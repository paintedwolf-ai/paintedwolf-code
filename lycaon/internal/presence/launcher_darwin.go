package presence

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <stdlib.h>
#include "launcher_darwin.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// VerifyLauncher confirms that the process that started this engine is the
// desktop app named by identifier, signed by this engine's own team.
func VerifyLauncher(identifier string) error {
	name := C.CString(identifier)
	defer C.free(unsafe.Pointer(name))
	reason := make([]byte, 256)
	if C.pw_presence_verify_parent(name, (*C.char)(unsafe.Pointer(&reason[0])), C.size_t(len(reason))) != 0 {
		return fmt.Errorf("%w: %s", ErrLauncherUnverified, C.GoString((*C.char)(unsafe.Pointer(&reason[0]))))
	}
	return nil
}
