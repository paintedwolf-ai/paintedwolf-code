//go:build darwin || linux

package fseffect

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// DirectoryPath reports the held directory's current native address within this root.
func (r *ReadRoot) DirectoryPath(dir *os.File) (string, error) {
	if err := r.validateDirectoryRoot(); err != nil {
		return "", err
	}
	path, err := readDirectoryPath(dir)
	if err != nil {
		return r.directoryPathFromName(dir)
	}
	return r.directoryPathFromNative(dir, path)
}

func (r *ReadRoot) directoryPathFromNative(dir *os.File, path string) (string, error) {
	if _, ok := lexicalRelUnder(r.root.path, path); !ok {
		if exact, err := r.directoryPathFromName(dir); err == nil {
			return exact, nil
		}
		return "", fmt.Errorf("%w: held directory moved outside root", ErrSymlink)
	}
	return path, nil
}

// directoryPathFromName validates identity without relying on descriptor-path support.
func (r *ReadRoot) directoryPathFromName(dir *os.File) (string, error) {
	rel, ok := lexicalRelUnder(r.root.path, dir.Name())
	if !ok {
		return "", ErrSymlink
	}
	if err := r.validateDirectoryRoot(); err != nil {
		return "", err
	}
	fd, err := unix.FcntlInt(uintptr(r.root.fd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	r.root.readOpens.Add(1)
	r.root.identityOpened()
	current := os.NewFile(uintptr(fd), r.root.path)
	defer func() { _ = current.Close(); r.root.identityActive.Add(-1) }()
	if rel != "." {
		for _, name := range strings.Split(filepath.ToSlash(rel), "/") {
			if name == "" || name == "." || name == ".." {
				return "", ErrInvalidPath
			}
			next, err := unix.Openat(int(current.Fd()), name, metadataOpenFlags(true)|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if err != nil {
				if err == unix.ELOOP || err == unix.ENOTDIR {
					return "", fmt.Errorf("%w: canonical directory identity changed", ErrSymlink)
				}
				return "", &fs.PathError{Op: "validate effect directory", Path: name, Err: err}
			}
			r.root.readOpens.Add(1)
			r.root.identityOpened()
			nextFile := os.NewFile(uintptr(next), filepath.Join(current.Name(), name))
			_ = current.Close()
			r.root.identityActive.Add(-1)
			current = nextFile
		}
	}
	expected, err := dir.Stat()
	if err != nil {
		return "", err
	}
	actual, err := current.Stat()
	if err != nil {
		return "", err
	}
	if !os.SameFile(expected, actual) {
		return "", ErrSymlink
	}
	return current.Name(), nil
}

// DirectoryIdentityDescriptors reports this root's identity-validation scratch peak and held count.
func (r *ReadRoot) DirectoryIdentityDescriptors() (peak, held int64) {
	return r.root.identityPeak.Load(), r.root.identityActive.Load()
}

// OpenChildDirectory opens an immediate directory relative to a held parent.
// Links use the contained resolver from the parent's current native address.
func (r *ReadRoot) OpenChildDirectory(dir *os.File, name string) (*os.File, error) {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return nil, ErrInvalidPath
	}
	parent, err := r.DirectoryPath(dir)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err == nil {
		r.root.readOpens.Add(1)
		return os.NewFile(uintptr(fd), filepath.Join(parent, name)), nil
	}
	if err != unix.ELOOP && err != unix.ENOTDIR {
		return nil, &fs.PathError{Op: "open effect directory", Path: name, Err: err}
	}
	rel, err := filepath.Rel(r.root.path, filepath.Join(parent, name))
	if err != nil {
		return nil, err
	}
	return r.OpenExact(rel)
}

// DescriptorOpens counts successful read descriptor opens and duplicates beneath this admitted root.
func (r *ReadRoot) DescriptorOpens() int64 { return r.root.readOpens.Load() }

func (r *openedRoot) identityOpened() {
	active := r.identityActive.Add(1)
	for {
		peak := r.identityPeak.Load()
		if active <= peak || r.identityPeak.CompareAndSwap(peak, active) {
			return
		}
	}
}

func (r *ReadRoot) validateDirectoryRoot() error {
	var held, named unix.Stat_t
	if err := unix.Fstat(r.root.fd, &held); err != nil {
		return err
	}
	if err := unix.Lstat(r.root.path, &named); err != nil {
		return err
	}
	if named.Mode&unix.S_IFMT != unix.S_IFDIR || named.Dev != held.Dev || named.Ino != held.Ino {
		return ErrSymlink
	}
	return nil
}
