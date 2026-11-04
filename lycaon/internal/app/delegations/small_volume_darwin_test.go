package delegations

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
)

// smallVolume attaches a private disk image and detaches it by its own device.
func smallVolume(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lycaon-full-*")
	testutil.FailErr(t, "volume dir", err)
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	image, mount := filepath.Join(dir, "volume.dmg"), filepath.Join(dir, "mount")
	testutil.FailErr(t, "mount dir", os.Mkdir(mount, 0o755))
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "/usr/bin/hdiutil", "create", "-quiet", "-size", "16m", "-fs", "HFS+", "-volname", "lycaonfull", image).CombinedOutput(); err != nil {
		t.Fatalf("create image: %v: %s", err, out)
	}
	out, err := exec.CommandContext(ctx, "/usr/bin/hdiutil", "attach", "-nobrowse", "-noverify", "-mountpoint", mount, image).CombinedOutput()
	if err != nil {
		t.Fatalf("attach image: %v: %s", err, out)
	}
	device := regexp.MustCompile(`/dev/disk[0-9]+`).Find(bytes.TrimSpace(out))
	if device == nil {
		t.Fatalf("attach reported no device: %s", out)
	}
	t.Cleanup(func() {
		detach, stop := context.WithTimeout(context.Background(), time.Minute)
		defer stop()
		if out, err := exec.CommandContext(detach, "/usr/bin/hdiutil", "detach", "-force", string(device)).CombinedOutput(); err != nil {
			t.Errorf("detach %s: %v: %s", device, err, out)
		}
	})
	return mount
}
