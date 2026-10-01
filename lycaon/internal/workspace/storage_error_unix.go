//go:build darwin || linux

package workspace

import (
	"errors"

	"golang.org/x/sys/unix"
)

func isStorageExhausted(err error) bool {
	return errors.Is(err, unix.ENOSPC) || errors.Is(err, unix.EDQUOT)
}
