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
	store := NewAt(path)

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

	vault, err := credentialstore.OpenFile(credentialstore.Slot{
		Path: path, Namespace: credentialstore.NamespaceProviders, Context: credentialContext,
	}, nil)
	testutil.FailErr(t, "reopen credential vault", err)
	reloaded := &Store{store: vault}
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
	store := NewAt(t.TempDir() + "/credential-vault.age")
	if err := store.Set("openai", "   "); err == nil {
		t.Fatal("empty API key was accepted")
	}
	if _, ok := store.Get("openai"); ok {
		t.Fatal("empty API key was stored")
	}
}
