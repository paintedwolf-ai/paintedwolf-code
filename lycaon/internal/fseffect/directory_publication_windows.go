//go:build windows

package fseffect

import (
	"os"

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
