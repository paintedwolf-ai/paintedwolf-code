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

// TestPath uses held non-reparse parents and FILE_OPEN only. Access checks ask
// the OS for the effective token's exact capability, then close the handle;
// acquiring that handle never creates bytes or grants a later operation.
func (r *ReadRoot) TestPath(rel, operator string) (bool, error) {
	requireDirectory := strings.HasSuffix(rel, "/") || strings.HasSuffix(rel, `\`)
	clean, err := cleanReadLocation(Location{Root: r.path, Rel: rel})
	if err != nil {
		return false, err
	}
	access := uint32(windows.FILE_READ_ATTRIBUTES)
	switch operator {
	case "-r":
		access |= windows.FILE_READ_DATA
	case "-w":
		access |= windows.FILE_WRITE_DATA
	case "-x":
		access |= windows.FILE_EXECUTE
	case "-e", "-f", "-d", "-L":
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
		if operator == "-L" && !requireDirectory {
			handle, err = ntOpenLeaf(parent.handle, parent.name, access)
		} else {
			handle, err = ntOpenRelative(parent.handle, parent.name, access, windows.FILE_OPEN, 0)
		}
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return false, nil
		}
		if (operator == "-r" || operator == "-w" || operator == "-x") && errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return false, nil
		}
		return false, err
	}
	f := os.NewFile(uintptr(handle), filepath.Join(r.path, clean.Rel))
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return false, err
	}
	if requireDirectory && !info.IsDir() {
		return false, nil
	}
	switch operator {
	case "-f":
		return info.Mode().IsRegular(), nil
	case "-d":
		return info.IsDir(), nil
	case "-L":
		return info.Mode()&os.ModeSymlink != 0, nil
	}
	return true, nil
}
