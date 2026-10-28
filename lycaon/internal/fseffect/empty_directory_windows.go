//go:build windows

package fseffect

import (
	"errors"
	"golang.org/x/sys/windows"
)

func RemoveEmptyDirectoryGuarded(loc Location, expected string) error {
	if expected == "" {
		return ErrPostcondition
	}
	err := Remove(RemoveRequest{Location: loc, directoryIdentity: expected})
	if errors.Is(err, windows.ERROR_DIR_NOT_EMPTY) {
		return ErrDirectoryNotEmpty
	}
	return err
}
