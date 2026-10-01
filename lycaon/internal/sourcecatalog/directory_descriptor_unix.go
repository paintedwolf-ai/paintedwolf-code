//go:build darwin || linux

package sourcecatalog

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func openDirectoryFile(root *os.Root, name string) (*os.File, error) {
	// O_DIRECTORY validates the opened descriptor without a second path traversal.
	file, err := root.OpenFile(name, os.O_RDONLY|unix.O_DIRECTORY, 0)
	if errors.Is(err, unix.ENOTDIR) {
		return nil, os.ErrInvalid
	}
	return file, err
}

func openStructuralScanDirectoryFile(root *os.Root, name string) (*os.File, error) {
	clean := filepath.ToSlash(filepath.Clean(name))
	if clean == "." || clean == "" {
		return root.OpenFile(".", os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	}
	if !strings.Contains(clean, "/") {
		if clean == ".." {
			return nil, os.ErrPermission
		}
		return root.OpenFile(filepath.FromSlash(clean), os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	}
	current, err := root.OpenFile(".", os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	components := strings.Split(clean, "/")
	for _, component := range components {
		if component == "" || component == "." || component == ".." {
			_ = current.Close()
			return nil, os.ErrPermission
		}
		fd, openErr := unix.Openat(int(current.Fd()), component, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		_ = current.Close()
		if errors.Is(openErr, unix.ENOTDIR) || errors.Is(openErr, unix.ELOOP) {
			return nil, os.ErrInvalid
		}
		if openErr != nil {
			return nil, openErr
		}
		current = os.NewFile(uintptr(fd), component)
	}
	return current, nil
}

func structuralScanDirectoryCacheSupported() bool { return true }

func openStructuralScanCacheRootFile(root *os.Root) (*os.File, error) {
	return root.OpenFile(".", os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
}

func duplicateStructuralScanDirectoryFile(file *os.File) (*os.File, error) {
	conn, err := file.SyscallConn()
	if err != nil {
		return nil, err
	}
	var fd int
	var duplicateErr error
	if err := conn.Control(func(original uintptr) {
		fd, duplicateErr = unix.FcntlInt(original, unix.F_DUPFD_CLOEXEC, 0)
	}); err != nil {
		return nil, err
	}
	if duplicateErr != nil {
		return nil, duplicateErr
	}
	return os.NewFile(uintptr(fd), file.Name()), nil
}

func openStructuralScanChildDirectoryFile(parent *os.File, name string) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.Contains(name, "/") {
		return nil, os.ErrPermission
	}
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP) {
		return nil, os.ErrInvalid
	}
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), name), nil
}

func structuralScanPathMatchesOpenDirectory(rootFile *os.File, dir string, file *os.File) (bool, error) {
	var current unix.Stat_t
	if err := unix.Fstatat(int(rootFile.Fd()), filepath.FromSlash(dir), &current, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false, err
	}
	var opened unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &opened); err != nil {
		return false, err
	}
	return current.Dev == opened.Dev && current.Ino == opened.Ino, nil
}

func normalizeStructuralScanDirectoryOpenError(err error) error {
	if errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP) {
		return os.ErrInvalid
	}
	return err
}

// A duplicate reads directory-entry kinds without eager per-entry metadata.
func directoryKinds(file *os.File) (*directoryKindReader, error) {
	duplicate, err := duplicateStructuralScanDirectoryFile(file)
	if err != nil {
		return nil, err
	}
	return &directoryKindReader{file: duplicate}, nil
}
