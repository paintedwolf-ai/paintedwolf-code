//go:build darwin || linux

package fileclone

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openCloneSource(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()
		if err == nil {
			err = fmt.Errorf("clone source is not a regular file: %s", path)
		}
		return nil, err
	}
	return file, nil
}
