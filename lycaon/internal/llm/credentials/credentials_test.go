package credentials

import (
	"os"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCredentialStoreRoundTrip(t *testing.T) {
	path := t.TempDir() + "/credential-vault.age"
	store, err := Open(credentialstore.Slot{Path: path, Namespace: credentialstore.NamespaceProviders, Context: credentialContext})
	testutil.FailErr(t, "open provider credentials", err)

	if err := store.Set("openai", "sk-test-secret"); err != nil {
		testutil.FailErr(t, "store.Set failed", err)
	}

	data, err := os.ReadFile(path)
	testutil.FailErr(t, "read file", err)
	body := string(data)
	if !strings.HasPrefix(body, "age-encryption.org/v1") {
		t.Fatalf("credential vault does not have an age header: %q", body)
	}
	if strings.Contains(body, "sk-test-secret") || strings.Contains(body, "openai") {
		t.Fatal("credential vault exposed plaintext")
	}

	reloaded, err := Open(credentialstore.Slot{
		Path: path, Namespace: credentialstore.NamespaceProviders, Context: credentialContext,
	})
	testutil.FailErr(t, "reopen credential vault", err)
	key, ok := reloaded.Get("openai")
	if !ok || key.Value() != "sk-test-secret" {
		t.Fatalf("get = %q ok = %v", key, ok)
	}

	info, err := os.Stat(path)
	testutil.FailErr(t, "stat path", err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 0600", info.Mode().Perm())
	}

	if err := store.Delete("openai"); err != nil {
		testutil.FailErr(t, "store.Delete failed", err)
	}
	if _, ok := store.Get("openai"); ok {
		t.Fatal("expected key deleted")
	}
}

func TestCredentialStoreRejectsEmptyKey(t *testing.T) {
	store, err := Open(credentialstore.Slot{Path: t.TempDir() + "/credential-vault.age", Namespace: credentialstore.NamespaceProviders, Context: credentialContext})
	testutil.FailErr(t, "open provider credentials", err)
	if err := store.Set("openai", "   "); err == nil {
		t.Fatal("empty API key was accepted")
	}
	if _, ok := store.Get("openai"); ok {
		t.Fatal("empty API key was stored")
	}
}

func TestCredentialStoreRefusesInvalidVaultWithoutMutation(t *testing.T) {
	slot := credentialstore.Slot{Path: t.TempDir() + "/credential-vault.age", Namespace: credentialstore.NamespaceProviders, Context: credentialContext}
	before := []byte("invalid encrypted vault")
	testutil.FailErr(t, "write invalid vault", os.WriteFile(slot.Path, before, 0o600))
	if store, err := Open(slot); err == nil || store != nil {
		t.Fatal("invalid vault was accepted")
	}
	after, err := os.ReadFile(slot.Path)
	testutil.FailErr(t, "read refused vault", err)
	if string(after) != string(before) {
		t.Fatal("refused vault was mutated")
	}
}
