package hostcontracts

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	hostapi "github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestWriteDaemonManifestMode0600(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	// LYCAON_CONFIG_DIR outranks HOME when resolving the manifest directory.
	t.Setenv("LYCAON_CONFIG_DIR", filepath.Join(dir, ".config", "paintedwolf"))

	if err := hostapi.WriteDaemonManifest("127.0.0.1", 8787, os.Getpid()); err != nil {
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

func TestWriteAPITokenPersistsToVaultAndRemovesLegacyFile(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "paintedwolf")
	t.Setenv("HOME", dir)
	t.Setenv(configdir.EnvConfigDir, cfgDir)
	testutil.FailErr(t, "mkdir config", os.MkdirAll(cfgDir, 0o700))

	legacyPath := filepath.Join(cfgDir, hostapi.LegacyTokenFileName)
	testutil.FailErr(t, "seed legacy api.token", os.WriteFile(legacyPath, []byte("legacy-plaintext-token\n"), 0o600))

	// ReadAPIToken must not import or fall back to legacy api.token before vault write.
	if _, err := hostapi.ReadAPIToken(); err == nil {
		t.Fatal("ReadAPIToken unexpectedly fell back to legacy api.token")
	}

	vaultPath, err := hostapi.WriteAPIToken("secret-token")
	testutil.FailErr(t, "WriteAPIToken failed", err)
	if vaultPath != credentialstore.DefaultVaultPath(cfgDir) {
		t.Fatalf("vaultPath = %q, want %q", vaultPath, credentialstore.DefaultVaultPath(cfgDir))
	}
	info, err := os.Stat(vaultPath)
	testutil.FailErr(t, "stat vault path", err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", info.Mode().Perm())
	}
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy api.token remained on disk after vault persistence: %v", err)
	}

	got, err := hostapi.ReadAPIToken()
	if err != nil || got != "secret-token" {
		t.Fatalf("ReadAPIToken = %q err=%v, want %q", got, err, "secret-token")
	}
}

func TestWriteAPITokenPreservesLegacyFileOnVaultWriteFailureAndReportsUnlinkFailure(t *testing.T) {
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".config", "paintedwolf")
	t.Setenv("HOME", dir)
	t.Setenv(configdir.EnvConfigDir, cfgDir)
	testutil.FailErr(t, "mkdir config", os.MkdirAll(cfgDir, 0o700))

	legacyPath := filepath.Join(cfgDir, hostapi.LegacyTokenFileName)
	testutil.FailErr(t, "seed legacy api.token", os.WriteFile(legacyPath, []byte("legacy-keep\n"), 0o600))

	// Block credential-vault.age with a directory so vault open/write fails.
	vaultPath := credentialstore.DefaultVaultPath(cfgDir)
	testutil.FailErr(t, "block vault path", os.MkdirAll(vaultPath, 0o700))

	if _, err := hostapi.WriteAPIToken("fresh-token"); err == nil {
		t.Fatal("WriteAPIToken succeeded despite blocked vault path")
	}
	if data, err := os.ReadFile(legacyPath); err != nil || string(data) != "legacy-keep\n" {
		t.Fatalf("legacy api.token was altered on failed vault write: %q, %v", data, err)
	}

	// Remove vault blocker and replace legacyPath with a non-empty directory so os.Remove fails with non-ErrNotExist.
	testutil.FailErr(t, "remove vault blocker", os.Remove(vaultPath))
	testutil.FailErr(t, "remove legacy file", os.Remove(legacyPath))
	testutil.FailErr(t, "mkdir non-empty legacy dir", os.MkdirAll(filepath.Join(legacyPath, "child"), 0o700))

	if _, err := hostapi.WriteAPIToken("fresh-token"); err == nil {
		t.Fatal("WriteAPIToken ignored non-ErrNotExist legacy unlink failure")
	}
}

func TestReadDaemonManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)

	if err := hostapi.WriteDaemonManifest("127.0.0.1", 9999, 4242); err != nil {
		testutil.FailErr(t, "WriteDaemonManifest failed", err)
	}
	m, err := hostapi.ReadDaemonManifest()
	testutil.FailErr(t, "ReadDaemonManifest failed", err)
	if m.Host != "127.0.0.1" || m.Port != 9999 || m.PID != 4242 {
		t.Fatalf("manifest = %+v", m)
	}
}
