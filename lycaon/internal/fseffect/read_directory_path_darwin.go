package fseffect

import (
	"bytes"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// readDirectoryPath reads the descriptor's address on every call and returns
// known when it is unchanged, so one directory's entries allocate no path.
func readDirectoryPath(file *os.File, known string) (string, error) {
	var path [unix.PathMax]byte
	_, _, errno := unix.Syscall(unix.SYS_FCNTL, file.Fd(), unix.F_GETPATH, uintptr(unsafe.Pointer(&path[0])))
	if errno != 0 {
		return "", errno
	}
	address := path[:]
	if end := bytes.IndexByte(address, 0); end >= 0 {
		address = address[:end]
	}
	if string(address) == known {
		return known, nil
	}
	return string(address), nil
}
