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
	path, err := readDirectoryPath(dir)
	if err != nil {
		return r.directoryPathFromName(dir)
	}
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
	current, err := openReadWalk(r.root, rel)
	if err != nil {
		return "", err
	}
	defer current.Close()
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
