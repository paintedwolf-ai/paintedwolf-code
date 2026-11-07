//go:build darwin || linux

package workerplacement

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// folderOwnerUID is who a placement folder must belong to.
var folderOwnerUID = os.Geteuid

// openPrivateFolder opens path without following a link and proves through the
// held descriptor that it is a directory this user owns with no other access.
func openPrivateFolder(path string) (int, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return -1, err
		}
		// A link, a file, or a directory only someone else may enter.
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) {
			return -1, fmt.Errorf("%w: %s: %w", ErrFolderNotPrivate, filepath.Base(path), err)
		}
		return -1, err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	if int(stat.Uid) != folderOwnerUID() || stat.Mode&0o077 != 0 {
		_ = unix.Close(fd)
		return -1, fmt.Errorf("%w: %s: owner %d, mode %o", ErrFolderNotPrivate, filepath.Base(path), stat.Uid, stat.Mode&0o777)
	}
	return fd, nil
}

func verifyPrivateFolder(path string) error {
	fd, err := openPrivateFolder(path)
	if err != nil {
		return err
	}
	return unix.Close(fd)
}

// readPrivateFile reads through the descriptor that passed the check, so a swap
// of the folder's name cannot change what is read.
func readPrivateFile(folder, name string) ([]byte, error) {
	fd, err := openPrivateFolder(folder)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unix.Close(fd) }()
	file, err := unix.Openat(fd, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	handle := os.NewFile(uintptr(file), name)
	defer func() { _ = handle.Close() }()
	return io.ReadAll(handle)
}
