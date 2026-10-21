package credentialstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

// The items custodian is the macOS Keychain and the Linux identity service;
// these cases hold for both.

func custodiedVault(t *testing.T, items *memoryItems) (string, []byte) {
	t.Helper()
	path := filepath.Join(t.TempDir(), VaultBasename)
	provider := &itemsIdentityProvider{account: "test", description: "test custodian", items: items}
	backend := &namespaceBackend{vault: &vaultFile{path: path, provider: provider}, namespace: "providers"}
	testutil.FailErr(t, "store credential", backend.Set("openai", "sk-kept"))
	ciphertext, err := os.ReadFile(path)
	testutil.FailErr(t, "read ciphertext", err)
	return path, ciphertext
}

func TestLostCustodianKeyOffersAResetThatStartsAFreshVault(t *testing.T) {
	items := &memoryItems{}
	path, _ := custodiedVault(t, items)
	items.value = nil // the custodian answers that the key is gone
	provider := &itemsIdentityProvider{account: "test", description: "test custodian", items: items}
	vault := &vaultFile{path: path, provider: provider}
	backend := &namespaceBackend{vault: vault, namespace: "providers"}
	if _, err := backend.Snapshot(); !errors.Is(err, ErrIdentityMissing) || errors.Is(err, ErrCustodianUnavailable) {
		t.Fatalf("lost key: %v, want a missing identity", err)
	}
	testutil.FailErr(t, "reset after a lost key", vault.purge())
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("reset left the unopenable vault: %v", err)
	}
	fresh := &namespaceBackend{vault: &vaultFile{path: path, provider: provider}, namespace: "providers"}
	testutil.FailErr(t, "store after reset", fresh.Set("openai", "sk-new"))
	if snapshot, err := fresh.Snapshot(); err != nil || snapshot.Values["openai"] != "sk-new" {
		t.Fatalf("fresh vault = %v, %v", snapshot.Values, err)
	}
}

func TestUnreachableCustodianIsNotAMissingKeyAndPreservesTheVault(t *testing.T) {
	items := &memoryItems{}
	path, ciphertext := custodiedVault(t, items)
	unreachable := &memoryItems{value: items.value, readErr: errors.New("keychain interaction not allowed"), removeErr: errors.New("keychain interaction not allowed")}
	provider := &itemsIdentityProvider{account: "test", description: "test custodian", items: unreachable}
	vault := &vaultFile{path: path, provider: provider}
	backend := &namespaceBackend{vault: vault, namespace: "providers"}
	if _, err := backend.Snapshot(); !errors.Is(err, ErrCustodianUnavailable) || errors.Is(err, ErrIdentityMissing) || errors.Is(err, ErrVaultCorrupt) {
		t.Fatalf("unreachable custodian: %v, want custodian unavailable", err)
	}
	if err := vault.purge(); !errors.Is(err, ErrCustodianUnavailable) {
		t.Fatalf("purge while the custodian is unreachable: %v, want a refusal", err)
	}
	current, err := os.ReadFile(path)
	testutil.FailErr(t, "read preserved ciphertext", err)
	if !bytes.Equal(current, ciphertext) {
		t.Fatal("a refused purge changed the vault")
	}
	// Reachable again, the key and the credential are intact.
	restored := &namespaceBackend{vault: &vaultFile{path: path, provider: &itemsIdentityProvider{account: "test", description: "test custodian", items: items}}, namespace: "providers"}
	if snapshot, err := restored.Snapshot(); err != nil || snapshot.Values["openai"] != "sk-kept" {
		t.Fatalf("after recovery = %v, %v", snapshot.Values, err)
	}
}
