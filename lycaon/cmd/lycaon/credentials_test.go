package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lycaon/lycaon/internal/api"
	"github.com/lycaon/lycaon/internal/configdir"
	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestCredentialsPurgeRequiresConfirmationAndClearsEveryNamespace(t *testing.T) {
	t.Setenv(configdir.EnvConfigDir, t.TempDir())
	slots, err := allCredentialSlots()
	testutil.FailErr(t, "resolve credential slots", err)

	for _, slot := range slots {
		store, openErr := credentialstore.Open(slot, nil)
		testutil.FailErr(t, "open "+slot.Context, openErr)
		testutil.FailErr(t, "seed "+slot.Context, store.Set("fixture", "secret"))
	}

	if err := runCredentials([]string{"purge"}); err == nil {
		t.Fatal("purge without --yes succeeded")
	}
	for _, slot := range slots {
		store, openErr := credentialstore.Open(slot, nil)
		testutil.FailErr(t, "reopen after refusal "+slot.Context, openErr)
		if _, ok := store.Get("fixture"); !ok {
			t.Fatalf("refused purge removed %s credentials", slot.Context)
		}
	}

	testutil.FailErr(t, "confirmed purge", runCredentials([]string{"purge", "--yes"}))
	for _, slot := range slots {
		store, openErr := credentialstore.Open(slot, nil)
		testutil.FailErr(t, "reopen after purge "+slot.Context, openErr)
		if _, ok := store.Get("fixture"); ok {
			t.Fatalf("confirmed purge left %s credentials", slot.Context)
		}
	}
}

func TestTokenPrintReadsVaultAndNeverMutatesMissingOrLegacyState(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, dir)
	t.Setenv("LYCAON_API_TOKEN", "")

	var out bytes.Buffer
	if err := runToken([]string{"print"}, &out); err == nil {
		t.Fatal("token print succeeded when vault did not exist")
	}
	if out.Len() != 0 {
		t.Fatalf("failed token print wrote stdout: %q", out.String())
	}
	if _, err := os.Stat(credentialstore.DefaultVaultPath(dir)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed token print created vault file: %v", err)
	}

	// Seeding legacy api.token must not satisfy token print.
	legacyPath := filepath.Join(dir, api.LegacyTokenFileName)
	testutil.FailErr(t, "seed legacy api.token", os.WriteFile(legacyPath, []byte("legacy-token\n"), 0o600))
	if err := runToken([]string{"print"}, &out); err == nil {
		t.Fatal("token print fell back to legacy api.token")
	}

	// Persist fresh token in vault; legacy api.token is unlinked and token print returns the vault token.
	_, err := api.WriteAPIToken("vault-bearer-token-123")
	testutil.FailErr(t, "WriteAPIToken", err)
	if _, err := os.Stat(legacyPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy api.token remained after WriteAPIToken: %v", err)
	}

	out.Reset()
	testutil.FailErr(t, "token print from vault", runToken([]string{"print"}, &out))
	if got := out.String(); got != "vault-bearer-token-123\n" {
		t.Fatalf("token print stdout = %q, want %q", got, "vault-bearer-token-123\n")
	}

	// LYCAON_API_TOKEN takes precedence over the stored vault token.
	t.Setenv("LYCAON_API_TOKEN", "  env-override-token  ")
	out.Reset()
	testutil.FailErr(t, "token print from env", runToken([]string{"print"}, &out))
	if got := out.String(); got != "env-override-token\n" {
		t.Fatalf("token print env stdout = %q, want %q", got, "env-override-token\n")
	}
}

func TestTokenPrintRejectsInvalidArgumentsAndCorruptVault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(configdir.EnvConfigDir, dir)
	t.Setenv("LYCAON_API_TOKEN", "")

	var out bytes.Buffer
	for _, badArgs := range [][]string{nil, {}, {"rotate"}, {"print", "--extra"}} {
		if err := runToken(badArgs, &out); err == nil {
			t.Fatalf("runToken(%v) unexpectedly succeeded", badArgs)
		}
	}

	vaultPath := credentialstore.DefaultVaultPath(dir)
	testutil.FailErr(t, "write corrupt vault", os.WriteFile(vaultPath, []byte("not-an-age-vault"), 0o600))
	if err := runToken([]string{"print"}, &out); err == nil {
		t.Fatal("token print succeeded on corrupt vault")
	}
	if out.Len() != 0 {
		t.Fatalf("failed token print wrote stdout: %q", out.String())
	}
}
