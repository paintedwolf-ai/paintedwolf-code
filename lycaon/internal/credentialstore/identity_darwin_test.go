package credentialstore

import (
	"errors"
	"fmt"
	"testing"
)

func TestKeychainStatusesMapToCustodianSentinels(t *testing.T) {
	if !errors.Is(keychainNotFound, errIdentityMissing) || !errors.Is(keychainDuplicate, errIdentityExists) {
		t.Fatal("keychain statuses do not map to the custodian sentinels")
	}
	for _, status := range []KeychainStatus{-25308, keychainMissingEntitlement, -26275} {
		if errors.Is(status, errIdentityMissing) || errors.Is(status, errIdentityExists) {
			t.Fatalf("status %d reads as missing or duplicate", status)
		}
	}
	var status KeychainStatus
	if !errors.As(fmt.Errorf("wrapped: %w", keychainMissingEntitlement), &status) || status != -34018 {
		t.Fatalf("wrapped entitlement status = %d", status)
	}
	if errors.As(errors.New("plain error"), &status) {
		t.Fatal("plain error classified as a Keychain status")
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

func TestKeychainLossIsAMissingKeyAndALockedKeychainIsUnavailable(t *testing.T) {
	missing := &itemsIdentityProvider{account: "a", description: "macOS Keychain", items: &memoryItems{readErr: keychainNotFound}}
	if _, err := missing.LoadOrCreate(true); !errors.Is(err, ErrIdentityMissing) || errors.Is(err, ErrCustodianUnavailable) {
		t.Fatalf("lost Keychain item: %v, want a missing key", err)
	}
	for _, status := range []KeychainStatus{-25308, keychainMissingEntitlement} {
		locked := &itemsIdentityProvider{account: "a", description: "macOS Keychain", items: &memoryItems{readErr: status, removeErr: status}}
		if _, err := locked.LoadOrCreate(true); !errors.Is(err, ErrCustodianUnavailable) || errors.Is(err, ErrIdentityMissing) {
			t.Fatalf("Keychain status %d: %v, want custodian unavailable", status, err)
		}
		if err := locked.Purge(); !errors.Is(err, ErrCustodianUnavailable) {
			t.Fatalf("Keychain status %d purge: %v, want a refusal", status, err)
		}
	}
}
