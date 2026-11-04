package delegations

import (
	"bytes"
	"context"
	"os/exec"
	"path/filepath"
	"regexp"
	"testing"
	"time"
)

func ranInOwnMountNamespace(*testing.T) bool { return false }

// mountSmallVolume attaches a private disk image at path and detaches it by its own device.
func mountSmallVolume(t *testing.T, path string) {
	t.Helper()
	image := filepath.Join(t.TempDir(), "volume.dmg")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if out, err := exec.CommandContext(ctx, "/usr/bin/hdiutil", "create", "-quiet", "-size", "16m", "-fs", "HFS+", "-volname", "lycaonfull", image).CombinedOutput(); err != nil {
		t.Fatalf("create image: %v: %s", err, out)
	}
	out, err := exec.CommandContext(ctx, "/usr/bin/hdiutil", "attach", "-nobrowse", "-noverify", "-mountpoint", path, image).CombinedOutput()
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
}
