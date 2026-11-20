package osprocess

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <stdlib.h>
#include <sys/types.h>
int pw_spawn_verified_child(const char *path, const char *cdhash, const int *fds, int count, pid_t *pid);
*/
import "C"

import (
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

// SpawnVerifiedChild resumes a child only after its kernel code identity matches cdhash.
// The child inherits files at descriptors starting with 3 and an empty environment.
func SpawnVerifiedChild(path, cdhash string, files []*os.File) (*os.Process, error) {
	if _, err := hex.DecodeString(cdhash); len(cdhash) != 40 || err != nil {
		return nil, fmt.Errorf("invalid child code directory hash")
	}
	if strings.IndexByte(path, 0) >= 0 {
		return nil, fmt.Errorf("invalid child executable path")
	}
	fds := make([]C.int, len(files))
	for i, file := range files {
		fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 3+len(fds))
		if err != nil {
			return nil, err
		}
		defer func() { _ = unix.Close(fd) }()
		fds[i] = C.int(fd)
	}
	exe, identity := C.CString(path), C.CString(cdhash)
	defer C.free(unsafe.Pointer(exe))
	defer C.free(unsafe.Pointer(identity))
	var pid C.pid_t
	if status := C.pw_spawn_verified_child(exe, identity, unsafe.SliceData(fds), C.int(len(fds)), &pid); status != 0 {
		return nil, fmt.Errorf("start verified child (status %d)", status)
	}
	return os.FindProcess(int(pid))
}
