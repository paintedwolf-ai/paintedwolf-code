//go:build darwin

package confine_test

import (
	"os"
	"testing"

	"golang.org/x/sys/unix"

	"github.com/lycaon/lycaon/internal/confine"
	"github.com/lycaon/lycaon/internal/testutil"
)

func stateTreeHelper(t *testing.T) string {
	t.Helper()
	if !confine.Available() {
		t.Skip("seatbelt sandbox unavailable")
	}
	self, err := os.Executable()
	testutil.FailErr(t, "locate confinement helper", err)
	return self
}

func platformSwapRename(oldpath, newpath string) error {
	return unix.RenamexNp(oldpath, newpath, unix.RENAME_SWAP)
}

func platformNoReplaceRename(oldpath, newpath string) error {
	return unix.RenamexNp(oldpath, newpath, unix.RENAME_EXCL)
}
