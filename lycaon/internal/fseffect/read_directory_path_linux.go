package fseffect

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// readDirectoryPath reads the descriptor's address on every call and returns
// known when it is unchanged, so one directory's entries allocate no path.
func readDirectoryPath(file *os.File, known string) (string, error) {
	var buffer [len("/proc/self/fd/") + 21]byte
	link := append(buffer[:0], "/proc/self/fd/"...)
	link = strconv.AppendUint(link, uint64(file.Fd()), 10)
	link = append(link, 0)
	var path [unix.PathMax]byte
	workingDirectory := unix.AT_FDCWD
	for {
		n, _, errno := unix.Syscall6(unix.SYS_READLINKAT, uintptr(workingDirectory), uintptr(unsafe.Pointer(&link[0])),
			uintptr(unsafe.Pointer(&path[0])), uintptr(len(path)), 0, 0)
		switch {
		case errno == syscall.EINTR:
			continue
		case errno != 0:
			return "", errno
		case int(n) == len(path):
			return "", syscall.ENAMETOOLONG
		}
		if string(path[:n]) == known {
			return known, nil
		}
		return string(path[:n]), nil
	}
}
