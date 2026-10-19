package main

import (
	"errors"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/hostlock"
)

func startupFailureCode(err error) string {
	code := "build_failed"
	switch {
	case errors.Is(err, hostlock.ErrStoreInstanceLocked):
		code = "store_locked"
	case errors.Is(err, credentialstore.ErrVaultLocked):
		code = "credential_vault_locked"
	case errors.Is(err, credentialstore.ErrVaultUninitialized):
		code = "credential_vault_uninitialized"
	case errors.Is(err, credentialstore.ErrVaultUnlockFailed):
		code = "credential_vault_unlock_failed"
	case errors.Is(err, credentialstore.ErrCustodianUnavailable):
		code = "credential_vault_custodian_unavailable"
	case errors.Is(err, credentialstore.ErrLauncherRefused):
		code = "credential_vault_launcher_refused"
	case errors.Is(err, credentialstore.ErrIdentityMissing):
		code = "credential_vault_identity_missing"
	case errors.Is(err, credentialstore.ErrVaultCorrupt):
		code = "credential_vault_corrupt"
	}
	return code
}
