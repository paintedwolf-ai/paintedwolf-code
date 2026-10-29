//go:build linux

package socketref

import (
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

func Platform() Adapter { return Adapter{Command: RetainedObject, Native: RetainedObject} }

// Open captures the socket object before its grant is offered for approval.
// O_PATH retains the final object even if any original name is later replaced.
func Open(path string) (*Reference, error) {
	fd, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "local service reference")
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFSOCK {
		_ = file.Close()
		return nil, fmt.Errorf("local service destination is not a socket")
	}
	resolved, err := os.Readlink(descriptorPath(file))
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return &Reference{file: file, path: resolved}, nil
}

func descriptorPath(file *os.File) string {
	return "/proc/self/fd/" + strconv.FormatUint(uint64(file.Fd()), 10)
}

func duplicate(file *os.File) (*os.File, error) {
	fd, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), "local service reference"), nil
}

func sameObject(a, b *Reference) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a == b {
		return a.file != nil
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	if a.file == nil || b.file == nil {
		return false
	}
	var first, second unix.Stat_t
	if unix.Fstat(int(a.file.Fd()), &first) != nil || unix.Fstat(int(b.file.Fd()), &second) != nil {
		return false
	}
	return first.Dev == second.Dev && first.Ino == second.Ino
}

func matchesPath(ref *Reference, path string) bool {
	ref.mu.RLock()
	defer ref.mu.RUnlock()
	if ref.file == nil {
		return false
	}
	var held, current unix.Stat_t
	if unix.Fstat(int(ref.file.Fd()), &held) != nil || unix.Stat(path, &current) != nil {
		return false
	}
	return held.Dev == current.Dev && held.Ino == current.Ino
}
