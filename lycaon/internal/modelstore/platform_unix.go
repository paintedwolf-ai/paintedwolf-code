//go:build darwin || linux

package modelstore

import (
	"math"
	"os"
	"syscall"
)

func lockFile(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX) }

func openBundlePayload(root *os.Root, name string) (*os.File, error) {
	// A replaced FIFO must not block bundle inspection.
	return root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
}

func defaultFreeBytes(path string) (int64, error) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return 0, err
	}
	if stat.Bsize <= 0 {
		return 0, ErrInsufficientSpace
	}
	if uint64(stat.Bavail) > uint64(math.MaxInt64)/uint64(stat.Bsize) {
		return math.MaxInt64, nil
	}
	return int64(stat.Bavail) * int64(stat.Bsize), nil
}
