//go:build linux

package launchguard

import (
	"os"
	"path/filepath"
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
		"PW_PROTECTED_LAUNCH=1", "PW_PROTECTED_LAUNCH=forged", "WEBKIT_INJECTED_BUNDLE_PATH=/tmp", "PYTHONPATH=/tmp", "malformed", "=empty", "DISPLAY=:0\x00x",
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
	file := filepath.Join(t.TempDir(), "file")
	testutil.FailErr(t, "write file", os.WriteFile(file, nil, 0o600))
	if got := DesktopEnvironment([]string{"HOME=" + file}, uint32(os.Getuid())); len(got) != 0 {
		t.Fatalf("a regular file crossed as HOME: %q", got)
	}
	link := filepath.Join(t.TempDir(), "link")
	testutil.FailErr(t, "symlink home", os.Symlink(t.TempDir(), link))
	if got := DesktopEnvironment([]string{"HOME=" + link}, uint32(os.Getuid())); len(got) != 0 {
		t.Fatalf("a symlink crossed as HOME: %q", got)
	}
}

func TestScrubBootstrapEnvironmentClearsProcessEnvironment(t *testing.T) {
	saved := os.Environ()
	t.Cleanup(func() {
		os.Clearenv()
		for _, entry := range saved {
			if k, v, ok := cutEnv(entry); ok {
				_ = os.Setenv(k, v)
			}
		}
		scrubbedSession = nil
		scrubbedOnce = false
	})

	os.Clearenv()
	_ = os.Setenv("HOME", "/nonexistent-caller-home")
	_ = os.Setenv("PATH", "/usr/bin:/bin")
	_ = os.Setenv("GODEBUG", "inittrace=1")
	_ = os.Setenv("GOTRACEBACK", "crash")
	_ = os.Setenv("GOCOVERDIR", "/tmp")
	_ = os.Setenv("LD_PRELOAD", "/tmp/evil.so")
	scrubbedSession = nil
	scrubbedOnce = false

	got := ScrubBootstrapEnvironment()
	if len(os.Environ()) != 0 {
		t.Fatalf("process environment not cleared: %v", os.Environ())
	}
	want := []string{"HOME=/nonexistent-caller-home", "PATH=/usr/bin:/bin"}
	if !slices.Equal(got, want) {
		t.Fatalf("ScrubBootstrapEnvironment() = %q, want %q", got, want)
	}
	if !slices.Equal(ScrubBootstrapEnvironment(), want) {
		t.Fatal("ScrubBootstrapEnvironment was not idempotent")
	}
	if os.Getuid() > 0 && !DropPrivilegesForError() {
		t.Fatal("DropPrivilegesForError failed for unprivileged test user")
	}
}

func cutEnv(entry string) (string, string, bool) {
	for i := 0; i < len(entry); i++ {
		if entry[i] == '=' {
			return entry[:i], entry[i+1:], true
		}
	}
	return "", "", false
}
