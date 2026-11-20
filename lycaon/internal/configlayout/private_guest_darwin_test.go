//go:build darwin

package configlayout

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
	"golang.org/x/sys/unix"
)

func TestPrivateGuestSnapshotsHoldOneGeneration(t *testing.T) {
	bundle, state := t.TempDir(), t.TempDir()
	for _, name := range []string{"pw-service-vm", "manifest.json", "kernel", "initramfs.gz"} {
		testutil.FailErr(t, "seed asset", os.WriteFile(filepath.Join(bundle, name), []byte(name), 0600))
	}
	testutil.FailErr(t, "seed helper identity", os.WriteFile(filepath.Join(bundle, "pw-service-vm.cdhash"), []byte(strings.Repeat("a", 40)), 0600))
	p, err := privateGuestPayload(bundle, state, "")
	testutil.FailErr(t, "stage runtime", err)
	defer p.Close()
	testutil.FailErr(t, "replace supplied image", os.WriteFile(filepath.Join(bundle, "kernel"), []byte("changed"), 0600))
	data, err := io.ReadAll(p.Boot[1])
	testutil.FailErr(t, "read held boot file", err)
	if string(data) != "kernel" {
		t.Fatalf("staged boot image = %q", data)
	}
	items, err := os.ReadDir(state)
	testutil.FailErr(t, "inspect capture directory", err)
	if len(items) != 0 {
		t.Fatalf("named boot snapshots: %v", items)
	}
	if p.Helper != filepath.Join(bundle, "pw-service-vm") {
		t.Fatalf("helper left signed package: %s", p.Helper)
	}
	held := p.Boot[1]
	p.Close()
	if _, err := held.Stat(); err == nil {
		t.Fatal("released boot file remained open")
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
	testutil.FailErr(t, "seed helper identity", os.WriteFile(filepath.Join(bundle, "pw-service-vm.cdhash"), []byte(strings.Repeat("a", 40)), 0600))
	if _, err := privateGuestPayload(bundle, state, bundle); err == nil {
		t.Fatal("accepted an unsigned production package")
	}
	entries, err := os.ReadDir(state)
	testutil.FailErr(t, "inspect failed preparations", err)
	if len(entries) != 0 {
		t.Fatalf("retained failed snapshots: %v", entries)
	}
}

func TestPrivateGuestSealsExactPackagedResources(t *testing.T) {
	app := filepath.Join(t.TempDir(), "Probe.app")
	contents := filepath.Join(app, "Contents")
	resources := filepath.Join(contents, "Resources", "service-guest")
	testutil.FailErr(t, "create resources", os.MkdirAll(resources, 0700))
	executables := filepath.Join(contents, "MacOS")
	testutil.FailErr(t, "create executable directory", os.Mkdir(executables, 0700))
	main, err := os.ReadFile("/usr/bin/true")
	testutil.FailErr(t, "read system fixture", err)
	testutil.FailErr(t, "write executable", os.WriteFile(filepath.Join(executables, "probe"), main, 0700))
	const metadata = `<?xml version="1.0" encoding="UTF-8"?>
<plist version="1.0"><dict><key>CFBundleIdentifier</key><string>dev.fixture.probe</string><key>CFBundleExecutable</key><string>probe</string><key>CFBundlePackageType</key><string>APPL</string></dict></plist>`
	testutil.FailErr(t, "write bundle metadata", os.WriteFile(filepath.Join(contents, "Info.plist"), []byte(metadata), 0600))
	resource := []byte("fixture code identity")
	testutil.FailErr(t, "write resource", os.WriteFile(filepath.Join(resources, "pw-service-vm.cdhash"), resource, 0600))
	output, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", app).CombinedOutput()
	if err != nil {
		t.Fatalf("sign test bundle: %v %s", err, output)
	}
	seal, release, err := openGuestResourceEnvelope(app)
	testutil.FailErr(t, "open signed app envelope", err)
	defer release()
	path := "Resources/service-guest/pw-service-vm.cdhash"
	testutil.FailErr(t, "verify captured bytes", verifyGuestResource(seal, path, resource))
	if err := verifyGuestResource(seal, path, []byte("another identity")); err == nil {
		t.Fatal("accepted a different packaged identity")
	}
}

func TestPrivateGuestBootFileRequiresExclusiveCustody(t *testing.T) {
	path := filepath.Join(t.TempDir(), "boot")
	testutil.FailErr(t, "seed boot file", os.WriteFile(path, []byte("boot"), 0600))
	rd, err := os.Open(path)
	testutil.FailErr(t, "retain boot input", err)
	defer rd.Close()
	if err := verifyUnlinkedBootFile(rd); err == nil {
		t.Fatal("accepted a named boot input")
	}
	writer, err := os.OpenFile(path, os.O_RDWR, 0)
	testutil.FailErr(t, "hold second descriptor", err)
	mapping, err := unix.Mmap(int(writer.Fd()), 0, 4, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	testutil.FailErr(t, "map boot input", err)
	testutil.FailErr(t, "unlink boot file", os.Remove(path))
	if err := verifyUnlinkedBootFile(rd); err == nil {
		t.Fatal("accepted another open descriptor")
	}
	testutil.FailErr(t, "release writer", writer.Close())
	if err := verifyUnlinkedBootFile(rd); err == nil {
		t.Fatal("accepted an outstanding writable mapping")
	}
	testutil.FailErr(t, "release mapping", unix.Munmap(mapping))
	testutil.FailErr(t, "establish exclusive custody", verifyUnlinkedBootFile(rd))
}
