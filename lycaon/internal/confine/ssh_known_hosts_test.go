package confine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
)

// Every build failure is reported as an empty path, which is indistinguishable
// from having no key material to mirror — and an absent mirror silently drops
// ssh to trust-on-first-use. So this asserts the file, not the call.
func TestBuildKnownHostsMirrorWritesCollectedMaterial(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(configdir.EnvConfigDir, filepath.Join(t.TempDir(), "config"))

	entry := "github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"
	sshDir := filepath.Join(home, ".ssh")
	testutil.FailErr(t, "create .ssh", os.MkdirAll(sshDir, 0o700))
	testutil.FailErr(t, "seed known_hosts",
		os.WriteFile(filepath.Join(sshDir, "known_hosts"), []byte(entry+"\n"), 0o600))

	path := buildKnownHostsMirror()
	if path == "" {
		t.Fatal("mirror path is empty: host key material was collected but never committed")
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path minted by the mirror under a test config dir
	testutil.FailErr(t, "read mirror", err)
	if !strings.Contains(string(data), entry) {
		t.Fatalf("mirror is missing the collected entry: %q", data)
	}
	info, err := os.Stat(path)
	testutil.FailErr(t, "stat mirror", err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mirror mode = %v, want 0600", info.Mode().Perm())
	}
}
