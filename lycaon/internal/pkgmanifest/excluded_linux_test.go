//go:build linux

package pkgmanifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/pkgmanifest"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOnlyANamedInstallerDirectoryIsExcludedFromThePayload(t *testing.T) {
	file, _ := fixture(t, nil)
	directory := filepath.Dir(file)
	if err := os.Chmod(directory, 0o755); err != nil {
		testutil.FailErr(t, "os.Chmod failed", err)
	}
	m, err := pkgmanifest.Load(file)
	testutil.FailErr(t, "pkgmanifest.Load failed", err)
	root, err := os.OpenRoot(directory)
	testutil.FailErr(t, "os.OpenRoot failed", err)
	defer func() { _ = root.Close() }()
	store := filepath.Join(directory, "generations")
	if err := os.MkdirAll(filepath.Join(store, "abc"), 0o755); err != nil {
		testutil.FailErr(t, "create directory", err)
	}
	if err := m.VerifyOwnedPayload(root, uint32(os.Geteuid())); err == nil || !strings.Contains(err.Error(), "unlisted payload object") {
		t.Fatalf("store without exclusion = %v", err)
	}
	for _, path := range []string{store, filepath.Join(store, "abc")} {
		if err := os.Chmod(path, 0o755); err != nil {
			testutil.FailErr(t, "os.Chmod failed", err)
		}
	}
	if err := m.VerifyOwnedPayload(root, uint32(os.Geteuid()), "generations"); err != nil {
		t.Fatalf("excluded store = %v", err)
	}
	for _, name := range []string{"bin", "manifest.json", "nested/dir", "..", ""} {
		if err := m.VerifyPayload(root, name); err == nil {
			t.Fatalf("exclusion %q accepted", name)
		}
	}
	if err := os.RemoveAll(store); err != nil {
		testutil.FailErr(t, "os.RemoveAll failed", err)
	}
	if err := os.WriteFile(store, []byte("not a directory"), 0o644); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	if err := m.VerifyPayload(root, "generations"); err == nil || !strings.Contains(err.Error(), "not a physical directory") {
		t.Fatalf("file at the store name = %v", err)
	}
}
