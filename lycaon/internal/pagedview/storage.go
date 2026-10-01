package pagedview

import (
	"errors"
	"syscall"
)

// StorageFull recognizes storage exhaustion without parsing diagnostic messages.
func StorageFull(err error) bool {
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT) || platformStorageFull(err) {
		return true
	}
	var code interface{ Code() int }
	return errors.As(err, &code) && code.Code()&255 == 13
}
