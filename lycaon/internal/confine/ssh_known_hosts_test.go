package confine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/testutil"
)

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

func TestMediatedProjectUsesExactHostKeyFile(t *testing.T) {
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	c := Confinement{Network: NetworkProxyOnly, ProjectID: "project-1"}
	path, err := PrepareSSHKnownHosts(c)
	testutil.FailErr(t, "prepare project keys", err)
	testutil.FailErr(t, "save key", os.WriteFile(path, []byte("fixture"), 0600))
	again, err := PrepareSSHKnownHosts(c)
	testutil.FailErr(t, "reopen project keys", err)
	data, err := os.ReadFile(again)
	testutil.FailErr(t, "read project key", err)
	if again != path || string(data) != "fixture" {
		t.Fatalf("key file %q contains %q", again, data)
	}
	for _, access := range []FilesystemAccess{AccessRead, AccessWrite} {
		fs, err := filesystemRules(c)
		testutil.FailErr(t, "compile project rules", err)
		exact, sibling := fs.Verdict(access, path), fs.Verdict(access, path+"2")
		if !exact.Allowed || sibling.Allowed {
			t.Errorf("%s project key verdict %+v, sibling verdict %+v", access, exact, sibling)
		}
	}
	c.ProjectID = "project-2"
	fs, err := filesystemRules(c)
	testutil.FailErr(t, "compile other project rules", err)
	if fs.Verdict(AccessWrite, path).Allowed {
		t.Fatal("another project can write the key file")
	}
	c.Network = NetworkDeny
	path, err = PrepareSSHKnownHosts(c)
	if path != "" || err != nil {
		t.Fatalf("offline key file = %q, %v", path, err)
	}
}
