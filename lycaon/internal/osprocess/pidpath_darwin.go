//go:build darwin && cgo

package osprocess

/*
#include <libproc.h>
#include <errno.h>
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// ExecutablePath is the kernel-recorded executable of pid.
func ExecutablePath(pid int) (string, error) {
	if pid <= 0 {
		return "", fmt.Errorf("invalid pid %d", pid)
	}
	var buffer [C.PROC_PIDPATHINFO_MAXSIZE]C.char
	n, err := C.proc_pidpath(C.int(pid), unsafe.Pointer(&buffer[0]), C.uint32_t(len(buffer)))
	if n <= 0 {
		return "", fmt.Errorf("executable of pid %d: %w", pid, err)
	}
	return C.GoStringN(&buffer[0], n), nil
}
