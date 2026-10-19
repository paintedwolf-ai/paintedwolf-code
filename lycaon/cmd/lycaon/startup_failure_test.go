package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/hostlock"
)

func TestStartupFailureSeparatesCustodyFromCorruption(t *testing.T) {
	for err, want := range map[error]string{
		hostlock.ErrStoreInstanceLocked:         "store_locked",
		credentialstore.ErrVaultLocked:          "credential_vault_locked",
		credentialstore.ErrVaultUninitialized:   "credential_vault_uninitialized",
		credentialstore.ErrVaultUnlockFailed:    "credential_vault_unlock_failed",
		credentialstore.ErrCustodianUnavailable: "credential_vault_custodian_unavailable",
		credentialstore.ErrLauncherRefused:      "credential_vault_launcher_refused",
		credentialstore.ErrIdentityMissing:      "credential_vault_identity_missing",
		credentialstore.ErrVaultCorrupt:         "credential_vault_corrupt",
		errors.New("unclassified"):              "build_failed",
	} {
		if got := startupFailureCode(fmt.Errorf("initialize credentials: %w", err)); got != want {
			t.Errorf("%v: got %q, want %q", err, got, want)
		}
	}
}
