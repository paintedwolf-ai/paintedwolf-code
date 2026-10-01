//go:build darwin

package browserengine

import (
	"errors"

	"golang.org/x/sys/unix"
)

func clearMacQuarantine(path string) error {
	err := unix.Removexattr(path, "com.apple.quarantine")
	if err != nil && (errors.Is(err, unix.ENOATTR) || errors.Is(err, unix.ENODATA) || errors.Is(err, unix.ENOENT)) {
		return nil
	}
	return err
}
