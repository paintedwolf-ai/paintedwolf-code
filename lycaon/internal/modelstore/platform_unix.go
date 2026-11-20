//go:build darwin || linux

package modelstore

import (
	"errors"
	"math"
	"math/bits"
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
	return availableDiskBytes(uint64(stat.Bavail), int64(stat.Bsize))
}

func availableDiskBytes(blocks uint64, blockSize int64) (int64, error) {
	if blockSize <= 0 {
		return 0, ErrInsufficientSpace
	}
	high, available := bits.Mul64(blocks, uint64(blockSize))
	if high != 0 || available > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(available), nil
}

func openPartial(root *os.Root, name string) (*os.File, error) {
	f, err := root.OpenFile(name, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err == nil && !privatePartial(info) {
		err = errors.New("modelstore: unsafe partial file")
	}
	if err != nil {
		return nil, errors.Join(err, f.Close())
	}
	return f, nil
}

func reclaimableBlocks(info os.FileInfo, links int) int64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || links < 1 || uint64(stat.Nlink) != uint64(links) {
		return 0
	}
	return min(info.Size(), stat.Blocks*512)
}

func privatePartial(info os.FileInfo) bool {
	return info.Mode().IsRegular() && info.Sys().(*syscall.Stat_t).Nlink == 1
}
