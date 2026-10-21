//go:build linux

package pkgmanifest_test

import (
	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"os"
	"path/filepath"
	"testing"
)

func TestOwnedPayloadIncludesRootSealAndEveryObject(t *testing.T) {
	file, _ := fixture(t, nil)
	m, err := pkgmanifest.Load(file)
	if err != nil {
		t.Fatal(err)
	}
	// t.TempDir honors the host umask; owned payload roots carry explicit modes.
	if err := os.Chmod(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(filepath.Dir(file))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := m.VerifyOwnedPayload(root, uint32(os.Geteuid())); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Dir(file), 0775); err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyOwnedPayload(root, uint32(os.Geteuid())); err == nil {
		t.Fatal("accepted group-writable payload root")
	}
	if err := os.Chmod(filepath.Dir(file), 0755); err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyOwnedPayload(root, uint32(os.Geteuid()+1)); err == nil {
		t.Fatal("accepted foreign owner")
	}
	if err := os.Chmod(file, 0666); err != nil {
		t.Fatal(err)
	}
	if err := m.VerifyOwnedPayload(root, uint32(os.Geteuid())); err == nil {
		t.Fatal("accepted writable inventory seal")
	}
}
