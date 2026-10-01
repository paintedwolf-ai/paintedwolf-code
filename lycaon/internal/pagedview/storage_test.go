package pagedview

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"
)

type storageCodeError int

func (e storageCodeError) Error() string { return "storage failure" }
func (e storageCodeError) Code() int     { return int(e) }

func TestStorageFullUsesStructuredCauses(t *testing.T) {
	for _, err := range []error{syscall.ENOSPC, syscall.EDQUOT, storageCodeError(13), storageCodeError(13 | 256), &os.PathError{Op: "write", Path: "cache", Err: syscall.ENOSPC}} {
		if !StorageFull(fmt.Errorf("prepare source: %w", err)) {
			t.Errorf("storage exhaustion not recognized: %v", err)
		}
	}
	for _, err := range []error{nil, ErrBudget, errors.New("no space left on device"), storageCodeError(5), syscall.EACCES} {
		if StorageFull(err) {
			t.Errorf("non-storage failure classified as full: %v", err)
		}
	}
}
