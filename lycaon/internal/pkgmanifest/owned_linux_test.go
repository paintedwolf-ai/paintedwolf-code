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
	root, err := os.OpenRoot(filepath.Dir(file))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := m.VerifyOwnedPayload(root, uint32(os.Geteuid())); err != nil {
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
