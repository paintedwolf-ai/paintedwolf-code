package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestOAuthTokenStoreEncryptedRoundTrip(t *testing.T) {
	slot := credentialstore.Slot{Path: filepath.Join(t.TempDir(), credentialstore.VaultBasename), Namespace: credentialstore.NamespaceMCPOAuth, Context: oauthRecordContext}
	store, err := OpenOAuthTokenStore(slot)
	testutil.FailErr(t, "open OAuth vault", err)
	want := OAuthTokenRecord{AccessToken: "access-canary", RefreshToken: "refresh-canary", ClientSecret: "client-canary", Resource: "https://example.com"}
	testutil.FailErr(t, "save OAuth tokens", store.Put("provider", want))
	data, err := os.ReadFile(slot.Path)
	testutil.FailErr(t, "read encrypted vault", err)
	if !strings.HasPrefix(string(data), "age-encryption.org/v1") {
		t.Fatal("vault is not age encrypted")
	}
	for _, secret := range []string{want.AccessToken, want.RefreshToken, want.ClientSecret} {
		if strings.Contains(string(data), secret) {
			t.Fatal("vault contains plaintext secret")
		}
	}
	reopened, err := OpenOAuthTokenStore(slot)
	testutil.FailErr(t, "reopen OAuth vault", err)
	if got, ok := reopened.Get("provider"); !ok || got != want {
		t.Fatal("OAuth record did not survive reopening")
	}
	testutil.FailErr(t, "delete OAuth tokens", reopened.Delete("provider"))
	empty, err := OpenOAuthTokenStore(slot)
	testutil.FailErr(t, "reopen after deletion", err)
	if empty.SignedIn("provider") {
		t.Fatal("deleted OAuth tokens survived reopening")
	}
}

func TestOAuthTokenStoreRefusesInvalidVaultWithoutMutation(t *testing.T) {
	slot := credentialstore.Slot{Path: filepath.Join(t.TempDir(), credentialstore.VaultBasename), Namespace: credentialstore.NamespaceMCPOAuth, Context: oauthRecordContext}
	before := []byte("invalid encrypted vault")
	testutil.FailErr(t, "write invalid vault", os.WriteFile(slot.Path, before, 0o600))
	if store, err := OpenOAuthTokenStore(slot); err == nil || store != nil {
		t.Fatal("invalid vault was accepted")
	}
	after, err := os.ReadFile(slot.Path)
	testutil.FailErr(t, "read refused vault", err)
	if string(after) != string(before) {
		t.Fatal("refused vault was mutated")
	}
}
