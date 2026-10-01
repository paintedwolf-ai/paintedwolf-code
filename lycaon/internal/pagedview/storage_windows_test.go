//go:build windows

package pagedview

import (
	"golang.org/x/sys/windows"
	"os"
	"testing"
)

func TestStorageFullWindowsErrors(t *testing.T) {
	for _, err := range []error{windows.ERROR_DISK_FULL, windows.ERROR_HANDLE_DISK_FULL, windows.ERROR_DISK_QUOTA_EXCEEDED} {
		if !StorageFull(&os.PathError{Op: "write", Path: "cache", Err: err}) {
			t.Errorf("disk error not recognized: %v", err)
		}
	}
}
