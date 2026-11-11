package credentialstore

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/testutil"
)

func TestVerifyDurableValidatesSlotAndReadback(t *testing.T) {
	context := "mcp-credentials"
	if err := VerifyDurable(Slot{Context: context}, "anthropic", "secret"); err == nil ||
		!strings.Contains(err.Error(), "credential vault path required") {
		t.Fatalf("empty path error = %v", err)
	}
	if err := VerifyDurable(Slot{Path: filepath.Join(t.TempDir(), VaultBasename), Context: context}, "anthropic", "secret"); err == nil ||
		!strings.Contains(err.Error(), "credential namespace required") {
		t.Fatalf("empty namespace error = %v", err)
	}
	slot := Slot{Path: filepath.Join(t.TempDir(), VaultBasename), Namespace: NamespaceMCPCredentials, Context: context}
	if err := VerifyDurable(slot, "  ", "secret"); err == nil ||
		!strings.Contains(err.Error(), "credential id required") {
		t.Fatalf("empty id error = %v", err)
	}
	backend := newDevelopmentBackend(slot.Path, slot.Namespace)
	testutil.FailErr(t, "seed vault", backend.Set("anthropic", "secret"))
	testutil.FailErr(t, "verify durable", VerifyDurable(slot, "anthropic", "secret"))
	if err := VerifyDurable(slot, "anthropic", "other"); err == nil ||
		!strings.Contains(err.Error(), "durable credential verification failed for "+NamespaceMCPCredentials+"/anthropic") {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestVerifyDurableFailsOnUnreadableVault(t *testing.T) {
	slot := Slot{Path: filepath.Join(t.TempDir(), VaultBasename), Namespace: NamespaceMCPCredentials, Context: "mcp-credentials"}
	if err := os.WriteFile(slot.Path, []byte("not an age document"), fileMode); err != nil {
		testutil.FailErr(t, "write file", err)
	}
	err := VerifyDurable(slot, "anthropic", "secret")
	if err == nil || !errors.Is(err, ErrVaultCorrupt) {
		t.Fatalf("corrupt vault error = %v", err)
	}
}

func TestLoadOrInitValidatesSlot(t *testing.T) {
	context := "mcp-oauth"
	if _, err := LoadOrInit(Slot{Context: context}, "anthropic", nil, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "credential vault path required") {
		t.Fatalf("empty path error = %v", err)
	}
	if _, err := LoadOrInit(Slot{Path: filepath.Join(t.TempDir(), VaultBasename), Context: context}, "anthropic", nil, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "credential namespace required") {
		t.Fatalf("empty namespace error = %v", err)
	}
	slot := Slot{Path: filepath.Join(t.TempDir(), VaultBasename), Namespace: NamespaceMCPOAuth, Context: context}
	if _, err := LoadOrInit(slot, " ", nil, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "credential id required") {
		t.Fatalf("empty id error = %v", err)
	}
}

func TestLoadOrInitInitializesAndReloads(t *testing.T) {
	slot := Slot{Path: filepath.Join(t.TempDir(), VaultBasename), Namespace: NamespaceMCPOAuth, Context: "mcp-oauth"}
	var validateCalls, initCalls, afterCalls int
	secret, err := LoadOrInit(slot, "anthropic",
		func(string) error { validateCalls++; return nil },
		func() (string, error) { initCalls++; return "generated", nil },
		func() error { afterCalls++; return nil })
	testutil.FailErr(t, "load or init", err)
	if secret.Value() != "generated" || validateCalls != 1 || initCalls != 1 || afterCalls != 1 {
		t.Fatalf("init path: secret = %q, validate = %d, init = %d, after = %d",
			secret.Value(), validateCalls, initCalls, afterCalls)
	}
	if _, err := os.Stat(slot.Path); err != nil {
		testutil.FailErr(t, "vault persisted", err)
	}
	existing, err := LoadOrInit(slot, "anthropic",
		func(string) error { validateCalls++; return nil },
		func() (string, error) { initCalls++; return "replacement", nil },
		func() error { afterCalls++; return nil })
	testutil.FailErr(t, "reload", err)
	if existing.Value() != "generated" || initCalls != 1 {
		t.Fatalf("existing path: secret = %q, init calls = %d", existing.Value(), initCalls)
	}
}

func TestLoadOrInitRejectsRefusedSecrets(t *testing.T) {
	slot := Slot{Path: filepath.Join(t.TempDir(), VaultBasename), Namespace: NamespaceMCPOAuth, Context: "mcp-oauth"}
	if _, err := LoadOrInit(slot, "anthropic", nil,
		func() (string, error) { return "", errors.New("generator failed") }, nil); err == nil ||
		!strings.Contains(err.Error(), "generator failed") {
		t.Fatalf("init failure = %v", err)
	}
	if _, err := LoadOrInit(slot, "anthropic",
		func(string) error { return errors.New("rejected") },
		func() (string, error) { return "generated", nil }, nil); err == nil ||
		!strings.Contains(err.Error(), "rejected") {
		t.Fatalf("validate failure = %v", err)
	}
	if _, err := os.Stat(slot.Path); !os.IsNotExist(err) {
		t.Fatal("refused init persisted a vault")
	}
}

func TestLoadOrInitRejectsExistingSecretWhenValidationFails(t *testing.T) {
	slot := Slot{Path: filepath.Join(t.TempDir(), VaultBasename), Namespace: NamespaceMCPOAuth, Context: "mcp-oauth"}
	backend := newDevelopmentBackend(slot.Path, slot.Namespace)
	testutil.FailErr(t, "seed vault", backend.Set("anthropic", "existing"))
	initCalled := false
	if _, err := LoadOrInit(slot, "anthropic",
		func(string) error { return errors.New("rejected") },
		func() (string, error) { initCalled = true; return "replacement", nil }, nil); err == nil ||
		!strings.Contains(err.Error(), "rejected") {
		t.Fatalf("existing validate failure = %v", err)
	}
	if initCalled {
		t.Fatal("init ran for an existing secret")
	}
}

func TestLoadOrInitRunsAfterVerifiedOnExistingSecret(t *testing.T) {
	slot := Slot{Path: filepath.Join(t.TempDir(), VaultBasename), Namespace: NamespaceMCPOAuth, Context: "mcp-oauth"}
	backend := newDevelopmentBackend(slot.Path, slot.Namespace)
	testutil.FailErr(t, "seed vault", backend.Set("anthropic", "existing"))
	if _, err := LoadOrInit(slot, "anthropic", nil, nil,
		func() error { return errors.New("side effect failed") }); err == nil ||
		!strings.Contains(err.Error(), "side effect failed") {
		t.Fatalf("afterVerified failure = %v", err)
	}
	secret, err := LoadOrInit(slot, "anthropic", nil, nil, func() error { return nil })
	testutil.FailErr(t, "reload", err)
	if secret.Value() != "existing" {
		t.Fatalf("secret = %q", secret.Value())
	}
}

func TestLoadOrInitRestoresVaultWhenDurableVerificationFails(t *testing.T) {
	slot := Slot{Path: filepath.Join(t.TempDir(), VaultBasename), Namespace: NamespaceMCPOAuth, Context: "mcp-oauth"}
	backend := newDevelopmentBackend(slot.Path, slot.Namespace)
	testutil.FailErr(t, "seed vault", backend.Set("retained", "kept"))
	identityPath := developmentIdentityPath(slot.Path)
	if _, err := os.Stat(identityPath); err != nil {
		testutil.FailErr(t, "development identity", err)
	}
	_, err := LoadOrInit(slot, "anthropic", nil, func() (string, error) {
		if rmErr := os.Remove(identityPath); rmErr != nil {
			return "", rmErr
		}
		return "generated", nil
	}, nil)
	if err == nil || !errors.Is(err, ErrVaultCorrupt) {
		t.Fatalf("verification failure = %v", err)
	}
	if _, statErr := os.Stat(slot.Path); statErr != nil {
		t.Fatalf("restore removed the vault: %v", statErr)
	}
}

func TestLoadOrInitFailsWhenExistingSecretCannotBeVerified(t *testing.T) {
	slot := Slot{Path: filepath.Join(t.TempDir(), VaultBasename), Namespace: NamespaceMCPOAuth, Context: "mcp-oauth"}
	backend := newDevelopmentBackend(slot.Path, slot.Namespace)
	testutil.FailErr(t, "seed vault", backend.Set("anthropic", "existing"))
	identityPath := developmentIdentityPath(slot.Path)
	_, err := LoadOrInit(slot, "anthropic", func(string) error {
		if rmErr := os.Remove(identityPath); rmErr != nil {
			return rmErr
		}
		return nil
	}, nil, nil)
	if err == nil || !errors.Is(err, ErrVaultCorrupt) {
		t.Fatalf("existing verification failure = %v", err)
	}
}

func TestLoadOrInitFailsWhenFreshVaultIdentityIsUnreadable(t *testing.T) {
	slot := Slot{Path: filepath.Join(t.TempDir(), VaultBasename), Namespace: NamespaceMCPOAuth, Context: "mcp-oauth"}
	identityPath := developmentIdentityPath(slot.Path)
	_, err := LoadOrInit(slot, "anthropic", nil, func() (string, error) {
		if writeErr := os.WriteFile(identityPath, []byte("not an identity document"), fileMode); writeErr != nil {
			return "", writeErr
		}
		return "generated", nil
	}, nil)
	if err == nil {
		t.Fatal("unreadable fresh identity was accepted")
	}
	if _, statErr := os.Stat(slot.Path); !os.IsNotExist(statErr) {
		t.Fatal("failed init persisted a vault")
	}
}

func TestSharedVaultWithoutProviderIsNotShared(t *testing.T) {
	path := filepath.Join(t.TempDir(), VaultBasename)
	orphan := sharedVault(path, nil)
	if orphan.provider != nil {
		t.Fatal("nil provider was replaced")
	}
	if orphan.path != filepath.Clean(path) {
		t.Fatalf("path = %q", orphan.path)
	}
	provided := sharedVault(path, newDevelopmentIdentityProvider(path))
	if orphan == provided {
		t.Fatal("orphan vault aliased a provided vault instance")
	}
	if vaultPathMutex(orphan.path) != vaultPathMutex(provided.path) {
		t.Fatal("orphan vault does not share lock coordination")
	}
}

func TestCustodyKeySharesOnlyMatchingAccounts(t *testing.T) {
	path := filepath.Join(t.TempDir(), VaultBasename)
	alice := &itemsIdentityProvider{mode: CustodyKeychain, account: "alice", description: "keychain"}
	if sharedVault(path, alice) != sharedVault(path, &itemsIdentityProvider{mode: CustodyKeychain, account: "alice", description: "keychain"}) {
		t.Fatal("matching keychain custody did not share the vault instance")
	}
	if sharedVault(path, alice) == sharedVault(path, &itemsIdentityProvider{mode: CustodyKeychain, account: "bob", description: "keychain"}) {
		t.Fatal("different keychain accounts shared the vault instance")
	}
	service := &itemsIdentityProvider{mode: CustodyIdentityService, account: "alice", description: "identity service"}
	if sharedVault(path, service) != sharedVault(path, &itemsIdentityProvider{mode: CustodyIdentityService, account: "alice", description: "identity service"}) {
		t.Fatal("matching identity service custody did not share the vault instance")
	}
	if sharedVault(path, service) == sharedVault(path, alice) {
		t.Fatal("different custody modes shared the vault instance")
	}
}
