package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWriteDaemonManifestMode0600(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// LYCAON_CONFIG_DIR outranks HOME when resolving the manifest directory.
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(dir, ".config", "paintedwolf"))

	if err := WriteDaemonManifest("127.0.0.1", 8787, os.Getpid()); err != nil {
		testutil.FailErr(t, "WriteDaemonManifest failed", err)
	}
	path := filepath.Join(dir, ".config", "paintedwolf", "daemon.json")
	info, err := os.Stat(path)
	testutil.FailErr(t, "stat path", err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read file", err)
	if strings.Contains(strings.ToLower(string(data)), "api_token") {
		t.Fatalf("daemon.json must not contain api token: %s", data)
	}
}

func TestWriteAPITokenFileMode0600(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	path, err := WriteAPITokenFile("secret-token")
	testutil.FailErr(t, "WriteAPITokenFile failed", err)
	info, err := os.Stat(path)
	testutil.FailErr(t, "stat path", err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", info.Mode().Perm())
	}
	got, err := ReadAPITokenFile()
	if err != nil || got != "secret-token" {
		t.Fatalf("read token: %q err=%v", got, err)
	}
}

func TestReadDaemonManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := WriteDaemonManifest("127.0.0.1", 9999, 4242); err != nil {
		testutil.FailErr(t, "WriteDaemonManifest failed", err)
	}
	m, err := ReadDaemonManifest()
	testutil.FailErr(t, "ReadDaemonManifest failed", err)
	if m.Host != "127.0.0.1" || m.Port != 9999 || m.PID != 4242 {
		t.Fatalf("manifest = %+v", m)
	}
}
