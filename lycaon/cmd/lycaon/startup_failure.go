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
	case errors.Is(err, credentialstore.ErrCustodianAbsent):
		code = "credential_vault_custodian_absent"
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
