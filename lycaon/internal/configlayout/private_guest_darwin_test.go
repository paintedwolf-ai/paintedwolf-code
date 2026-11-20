//go:build darwin

package configlayout

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestPrivateGuestSnapshotsHoldOneGeneration(t *testing.T) {
	bundle, state := t.TempDir(), t.TempDir()
	for _, name := range []string{"pw-service-vm", "manifest.json", "kernel", "initramfs.gz"} {
		testutil.FailErr(t, "seed asset", os.WriteFile(filepath.Join(bundle, name), []byte(name), 0600))
	}
	p, err := privateGuestPayload(bundle, state, "")
	testutil.FailErr(t, "stage runtime", err)
	defer p.Close()
	testutil.FailErr(t, "replace supplied image", os.WriteFile(filepath.Join(bundle, "kernel"), []byte("changed"), 0600))
	data, err := io.ReadAll(p.Boot[1])
	testutil.FailErr(t, "read held boot file", err)
	if string(data) != "kernel" {
		t.Fatalf("staged boot image = %q", data)
	}
	items, err := os.ReadDir(filepath.Dir(p.Helper))
	testutil.FailErr(t, "inspect snapshot", err)
	if len(items) != 1 || items[0].Name() != "pw-service-vm" {
		t.Fatalf("named snapshot files = %v", items)
	}
	p.Close()
	if _, err := os.Stat(p.Helper); !os.IsNotExist(err) {
		t.Fatalf("released helper still reachable: %v", err)
	}
}

func TestPrivateGuestRefusesSymlinkAndUnsealedManifest(t *testing.T) {
	bundle, state := t.TempDir(), t.TempDir()
	for _, name := range []string{"pw-service-vm", "manifest.json", "kernel"} {
		testutil.FailErr(t, "seed runtime", os.WriteFile(filepath.Join(bundle, name), []byte(name), 0600))
	}
	ramdisk := filepath.Join(bundle, "initramfs.gz")
	testutil.FailErr(t, "create linked boot asset", os.Symlink("kernel", ramdisk))
	if _, err := privateGuestPayload(bundle, state, ""); err == nil {
		t.Fatal("accepted a linked boot asset")
	}
	testutil.FailErr(t, "remove fixture link", os.Remove(ramdisk))
	testutil.FailErr(t, "seed ramdisk", os.WriteFile(ramdisk, []byte("ramdisk"), 0600))
	if _, err := privateGuestPayload(bundle, state, bundle); err == nil {
		t.Fatal("accepted an unsigned production package")
	}
	entries, err := os.ReadDir(state)
	testutil.FailErr(t, "inspect failed preparations", err)
	if len(entries) != 0 {
		t.Fatalf("retained failed snapshots: %v", entries)
	}
}
