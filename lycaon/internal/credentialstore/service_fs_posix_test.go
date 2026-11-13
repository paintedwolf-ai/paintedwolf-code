//go:build !windows

package credentialstore

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestLoadOrInitReportsUnexpectedVaultStatFailures(t *testing.T) {
	path := filepath.Join(t.TempDir(), VaultBasename)
	testutil.FailErr(t, "make the vault path a symlink loop", os.Symlink(path, path))
	slot := Slot{Path: path, Namespace: "ns", Context: "ctx"}
	if _, err := LoadOrInit(slot, "id", nil, func() (string, error) { return "secret", nil }, nil); err == nil {
		t.Fatal("LoadOrInit ignored a vault stat failure")
	}
}

func TestLoadOrInitFailsWhenTheVaultCannotBeCommitted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, VaultBasename)
	good := newDevelopmentIdentityProvider(path)
	_, err := good.LoadOrCreate(false)
	testutil.FailErr(t, "create custodian identity", err)
	testutil.FailErr(t, "pre-create the vault lock file", os.WriteFile(path+".lock", nil, 0o600))
	testutil.FailErr(t, "make the vault directory read-only", os.Chmod(dir, 0o500))
	defer func() { testutil.FailErr(t, "restore the vault directory", os.Chmod(dir, 0o700)) }()

	vault := &vaultFile{path: path, provider: good}
	if _, err := vault.loadOrInit(good, "ns", "id", nil,
		func() (string, error) { return "secret", nil }, nil); err == nil {
		t.Fatal("LoadOrInit ignored a vault commit failure")
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("vault was written despite the commit failure: %v", statErr)
	}
}
