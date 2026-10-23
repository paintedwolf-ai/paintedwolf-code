//go:build linux

package pkgmanifest_test

import (
	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/testutil"
	"os"
	"path/filepath"
	"testing"
)

func TestOwnedPayloadIncludesRootSealAndEveryObject(t *testing.T) {
	file, _ := fixture(t, nil)
	m, err := pkgmanifest.Load(file)
	testutil.FailErr(t, "pkgmanifest.Load failed", err)
	// t.TempDir honors the host umask; owned payload roots carry explicit modes.
	if err := os.Chmod(filepath.Dir(file), 0755); err != nil {
		testutil.FailErr(t, "os.Chmod failed", err)
	}
	root, err := os.OpenRoot(filepath.Dir(file))
	testutil.FailErr(t, "os.OpenRoot failed", err)
	defer root.Close()
	if err := m.VerifyOwnedPayload(root, uint32(os.Geteuid())); err != nil {
		testutil.FailErr(t, "m.VerifyOwnedPayload failed", err)
	}
	if err := os.Chmod(filepath.Dir(file), 0775); err != nil {
		testutil.FailErr(t, "os.Chmod failed", err)
	}
	if err := m.VerifyOwnedPayload(root, uint32(os.Geteuid())); err == nil {
		t.Fatal("accepted group-writable payload root")
	}
	if err := os.Chmod(filepath.Dir(file), 0755); err != nil {
		testutil.FailErr(t, "os.Chmod failed", err)
	}
	if err := m.VerifyOwnedPayload(root, uint32(os.Geteuid()+1)); err == nil {
		t.Fatal("accepted foreign owner")
	}
	if err := os.Chmod(file, 0666); err != nil {
		testutil.FailErr(t, "os.Chmod failed", err)
	}
	if err := m.VerifyOwnedPayload(root, uint32(os.Geteuid())); err == nil {
		t.Fatal("accepted writable inventory seal")
	}
}
