package hostidentity

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/uuid"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadOrCreateIsStable(t *testing.T) {
	dir := t.TempDir()
	first, err := LoadOrCreate(dir)
	testutil.FailErr(t, "create identity", err)
	second, err := LoadOrCreate(dir)
	testutil.FailErr(t, "reload identity", err)
	if first.HostID != second.HostID || !first.PublicKey.Equal(second.PublicKey) {
		t.Fatalf("identity changed across loads: %+v then %+v", first, second)
	}
	if len(first.PublicKey) != ed25519.PublicKeySize {
		t.Fatalf("public key length = %d", len(first.PublicKey))
	}
	if first.HostID != DeriveHostID(first.PublicKey) {
		t.Fatalf("host id %s is not derived from the public key", first.HostID)
	}
}

func TestHostIDIsVersion8UUID(t *testing.T) {
	public, _, err := ed25519.GenerateKey(nil)
	testutil.FailErr(t, "generate key", err)
	parsed, err := uuid.Parse(DeriveHostID(public))
	testutil.FailErr(t, "parse host id", err)
	if parsed.Version() != 8 || parsed.Variant() != uuid.RFC4122 {
		t.Fatalf("host id version=%d variant=%v", parsed.Version(), parsed.Variant())
	}
}

func TestKeyFileIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("profile ACLs govern the config directory on Windows")
	}
	dir := t.TempDir()
	_, err := LoadOrCreate(dir)
	testutil.FailErr(t, "create identity", err)
	info, err := os.Stat(filepath.Join(dir, FileName))
	testutil.FailErr(t, "stat key", err)
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("key mode = %o, want 600", mode)
	}
	entries, err := os.ReadDir(dir)
	testutil.FailErr(t, "read config dir", err)
	if len(entries) != 1 {
		t.Fatalf("staging files left behind: %v", entries)
	}
}

func TestCorruptKeyIsNeverReplaced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, FileName)
	testutil.FailErr(t, "write corrupt key", os.WriteFile(path, []byte("not a key"), 0o600))
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("corrupt key loaded")
	}
	data, err := os.ReadFile(path)
	testutil.FailErr(t, "reread key", err)
	if string(data) != "not a key" {
		t.Fatal("corrupt key was overwritten")
	}
}
