//go:build darwin

package testvolume

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// New attaches a fresh disk image. A host without hdiutil cannot run these tests.
// The second result is true when the caller should return; it never is on macOS.
func New(t *testing.T, kind Kind) (*Volume, bool) {
	t.Helper()
	if _, err := exec.LookPath("hdiutil"); err != nil {
		t.Skipf("hdiutil unavailable: %v", err)
	}
	fs := "APFS"
	if kind == NoClone {
		fs = "HFS+"
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temporary directory: %v", err)
	}
	image := filepath.Join(base, "volume.dmg")
	mount := filepath.Join(base, "mount")
	if err := os.Mkdir(mount, 0o755); err != nil {
		t.Fatalf("create mount point: %v", err)
	}
	hdiutil(t, "create", "-size", "32m", "-fs", fs, "-volname", "pwplacement", "-quiet", image)
	attach := func(t *testing.T) {
		hdiutil(t, "attach", image, "-nobrowse", "-owners", "on", "-mountpoint", mount, "-quiet")
	}
	attached := false
	v := &Volume{Root: mount}
	v.eject = func(t *testing.T) {
		hdiutil(t, "detach", mount, "-quiet")
		attached = false
	}
	v.remount = func(t *testing.T) {
		attach(t)
		attached = true
	}
	v.Remount(t)
	t.Cleanup(func() {
		if attached {
			if out, err := exec.Command("hdiutil", "detach", mount, "-force", "-quiet").CombinedOutput(); err != nil {
				t.Errorf("detach test volume: %v: %s", err, out)
			}
		}
	})
	return v, false
}

// MakeReadOnly remounts the volume read-only.
func (v *Volume) MakeReadOnly(t *testing.T) {
	t.Helper()
	image := filepath.Join(filepath.Dir(v.Root), "volume.dmg")
	v.Eject(t)
	hdiutil(t, "attach", image, "-nobrowse", "-readonly", "-mountpoint", v.Root, "-quiet")
	t.Cleanup(func() { _ = exec.Command("hdiutil", "detach", v.Root, "-force", "-quiet").Run() })
}

func hdiutil(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("hdiutil", args...).CombinedOutput(); err != nil {
		t.Fatalf("hdiutil %v: %v: %s", args, err, out)
	}
}
