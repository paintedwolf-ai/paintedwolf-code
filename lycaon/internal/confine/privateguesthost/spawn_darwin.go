package privateguesthost

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <stdlib.h>
#include <sys/types.h>
int pw_guest_spawn(const char *path, const char *cdhash, const int *fds, pid_t *pid);
*/
import "C"

import (
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"unsafe"
)

func spawnGuest(path, cdhash string, files []*os.File) (*os.Process, error) {
	var fds [4]C.int
	if len(files) != len(fds) {
		return nil, fmt.Errorf("private service requires %d activation descriptors", len(fds))
	}
	for i, file := range files {
		fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 16)
		if err != nil {
			return nil, err
		}
		defer unix.Close(fd)
		fds[i] = C.int(fd)
	}
	exe, identity := C.CString(path), C.CString(cdhash)
	defer C.free(unsafe.Pointer(exe))
	defer C.free(unsafe.Pointer(identity))
	var pid C.pid_t
	if status := C.pw_guest_spawn(exe, identity, &fds[0], &pid); status != 0 {
		return nil, fmt.Errorf("start verified private service helper (status %d)", status)
	}
	return os.FindProcess(int(pid))
}
