//go:build windows

package fseffect

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// ReadRoot names an admitted root; each read opens a held, non-reparse parent.
type ReadRoot struct{ path string }

// OpenReadRoot records the directory at path.
func OpenReadRoot(path string) (*ReadRoot, error) {
	clean, err := cleanReadLocation(Location{Root: path, Rel: "."})
	if err != nil {
		return nil, err
	}
	root, err := filepath.EvalSymlinks(clean.Root)
	if err != nil {
		return nil, err
	}
	return &ReadRoot{path: root}, nil
}

// Path is the root's canonical path.
func (r *ReadRoot) Path() string { return r.path }

// Open opens rel beneath the root.
func (r *ReadRoot) Open(rel string) (*os.File, error) {
	return OpenRead(Location{Root: r.path, Rel: rel})
}

// Close releases nothing; each read holds its own parent.
func (r *ReadRoot) Close() error { return nil }

// OpenRead opens a target relative to a held, non-reparse parent.
func OpenRead(loc Location) (*os.File, error) {
	clean, err := cleanReadLocation(loc)
	if err != nil {
		return nil, err
	}
	if clean.Rel == "." {
		root, err := filepath.EvalSymlinks(clean.Root)
		if err != nil {
			return nil, err
		}
		handle, err := openWindowsRoot(root, false)
		if err != nil {
			return nil, err
		}
		return os.NewFile(uintptr(handle), root), nil
	}
	parent, err := openWindowsParent(clean, false, false)
	if err != nil {
		return nil, err
	}
	f, err := openWindowsFile(parent, parent.name, windows.FILE_GENERIC_READ, windows.FILE_OPEN, 0)
	parent.close()
	return f, err
}
