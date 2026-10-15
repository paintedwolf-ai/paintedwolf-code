package credentialstore

import (
	"errors"
	"testing"
)

func TestKeychainStatusesMapToCustodianSentinels(t *testing.T) {
	if !errors.Is(keychainNotFound, errIdentityMissing) || !errors.Is(keychainDuplicate, errIdentityExists) {
		t.Fatal("keychain statuses do not map to the custodian sentinels")
	}
	for _, status := range []keychainStatus{-25308, keychainMissingEntitlement, -26275} {
		if errors.Is(status, errIdentityMissing) || errors.Is(status, errIdentityExists) {
			t.Fatalf("status %d reads as missing or duplicate", status)
		}
	}
	p := newKeychainIdentityProvider("/a/credential-vault.age").(*itemsIdentityProvider)
	if p.account != identityAccount("/a/credential-vault.age") {
		t.Fatalf("account = %q", p.account)
	}
}

func TestCredentialProtectionProbeRefusesIsolatedProvider(t *testing.T) {
	if err := VerifyIdentityProtection(); err == nil {
		t.Fatal("an isolated test process qualified the release credential provider")
	}
}
