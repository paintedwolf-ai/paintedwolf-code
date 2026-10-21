//go:build linux

package launchguard

import (
	"os"
	"slices"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestDesktopEnvironmentKeepsOnlySessionFacts(t *testing.T) {
	home := t.TempDir()
	got := DesktopEnvironment([]string{
		"HOME=" + home, "PATH=/usr/bin", "WAYLAND_DISPLAY=wayland-0", "LC_TIME=C", "LANG=C.UTF-8",
		"PATH=/attacker/bin", "LD_PRELOAD=/tmp/x.so", "GTK_MODULES=x", "GTK_PATH=/tmp", "GIO_EXTRA_MODULES=/tmp",
		"GDK_PIXBUF_MODULE_FILE=/tmp/x", "LYCAON_SANDBOX=off", "LYCAON_BYPASS_APPROVALS=1", "SSL_CERT_FILE=/tmp/ca",
		"WEBKIT_INJECTED_BUNDLE_PATH=/tmp", "PYTHONPATH=/tmp", "malformed", "=empty", "DISPLAY=:0\x00x",
	}, uint32(os.Getuid()))
	want := []string{"HOME=" + home, "PATH=/usr/bin", "WAYLAND_DISPLAY=wayland-0", "LC_TIME=C", "LANG=C.UTF-8"}
	if !slices.Equal(got, want) {
		t.Fatalf("desktop environment = %q, want %q", got, want)
	}
}

func TestDesktopEnvironmentRequiresAnOwnedHome(t *testing.T) {
	for _, home := range []string{"relative/home", "/proc/1", "/nonexistent-home-for-test", "/"} {
		if got := DesktopEnvironment([]string{"HOME=" + home}, uint32(os.Getuid())); len(got) != 0 {
			t.Fatalf("HOME %q crossed: %q", home, got)
		}
	}
	file := t.TempDir() + "/file"
	testutil.FailErr(t, "write file", os.WriteFile(file, nil, 0o600))
	if got := DesktopEnvironment([]string{"HOME=" + file}, uint32(os.Getuid())); len(got) != 0 {
		t.Fatalf("a regular file crossed as HOME: %q", got)
	}
}
