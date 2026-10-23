//go:build windows

package fseffect

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// Access uses held non-reparse parents and FILE_OPEN only. Access checks ask
// the OS for the effective token's exact capability, then close the handle;
// acquiring that handle never creates bytes or grants a later operation.
func (r *ReadRoot) Access(rel string, mode AccessMode, admit func(string) error) (bool, error) {
	requireDirectory := strings.HasSuffix(rel, "/") || strings.HasSuffix(rel, `\`)
	clean, err := cleanReadLocation(Location{Root: r.path, Rel: rel})
	if err != nil {
		return false, err
	}
	access := uint32(windows.FILE_READ_ATTRIBUTES)
	switch mode {
	case ReadAccess:
		access |= windows.FILE_READ_DATA
	case WriteAccess:
		access |= windows.FILE_WRITE_DATA
	case ExecuteAccess:
		access |= windows.FILE_EXECUTE
	default:
		return false, ErrInvalidPath
	}
	var handle windows.Handle
	if clean.Rel == "." {
		handle, err = openWindowsRootAccess(r.path, access)
	} else {
		var parent *windowsParent
		parent, err = openWindowsParent(clean, false, false)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
				return false, nil
			}
			return false, err
		}
		defer parent.close()
		handle, err = ntOpenRelative(parent.handle, parent.name, access, windows.FILE_OPEN, 0)
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return false, nil
		}
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return false, nil
		}
		return false, err
	}
	f := os.NewFile(uintptr(handle), filepath.Join(r.path, clean.Rel))
	defer func() { _ = f.Close() }()
	if admit != nil {
		if err := admit(f.Name()); err != nil {
			return false, err
		}
	}
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if requireDirectory && !info.IsDir() {
		return false, nil
	}

	return true, nil
}
