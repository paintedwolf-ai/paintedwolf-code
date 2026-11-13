//go:build windows

package fseffect

import (
	"os"
	"path/filepath"

	"github.com/lycaon/lycaon/internal/fspath"
	"golang.org/x/sys/windows"
)

func openGuardedDirectory(loc Location, expected string) (*os.File, error) {
	parent, err := openWindowsParent(loc, false, true)
	if err != nil {
		return nil, err
	}
	defer parent.close()
	handle, err := ntOpenRelative(parent.handle, parent.name, windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE, windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(handle), parent.name)
	identity, err := fspath.EntryIdentityHandle(handle)
	if err == nil && (expected == "" || identity != expected) {
		err = ErrPostcondition
	}
	if err != nil {
		_ = dir.Close()
		return nil, err
	}
	return dir, nil
}

// MkdirAll ensures a directory exists through held handles.
func MkdirAll(loc Location, _ os.FileMode) (createdCount int, err error) {
	clean, err := cleanReadLocation(loc)
	if err != nil {
		return createdCount, err
	}
	root, err := filepath.EvalSymlinks(clean.Root)
	if err != nil {
		return createdCount, err
	}
	handle, err := openWindowsRoot(root, clean.Rel != ".")
	if err != nil {
		return createdCount, err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	if clean.Rel == "." {
		return 0, nil
	}
	anyCreated := false
	for _, component := range splitWindowsRel(clean.Rel) {
		next, err := ntOpenRelative(handle, component,
			windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE,
			windows.FILE_OPEN, windows.FILE_DIRECTORY_FILE)
		created := false
		if err != nil && os.IsNotExist(err) {
			next, err = ntOpenRelative(handle, component,
				windows.FILE_GENERIC_READ|windows.FILE_GENERIC_WRITE,
				windows.FILE_CREATE, windows.FILE_DIRECTORY_FILE|windows.FILE_WRITE_THROUGH)
			created = err == nil
			if created {
				createdCount++
			}
		}
		if err != nil {
			return createdCount, err
		}
		if created {
			anyCreated = true
			if err := flushHandle(handle); err != nil {
				_ = windows.CloseHandle(next)
				return createdCount, err
			}
		}
		_ = windows.CloseHandle(handle)
		handle = next
	}
	if !anyCreated {
		return createdCount, nil
	}
	return createdCount, flushHandle(handle)
}
