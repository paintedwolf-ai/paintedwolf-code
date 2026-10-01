package secretmatch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func TestFingerprinterStableAndDeviceKeyed(t *testing.T) {
	first, err := NewFingerprinter([]byte(strings.Repeat("a", fingerprintKeyBytes)))
	testutil.FailErr(t, "construct first fingerprinter", err)
	second, err := NewFingerprinter([]byte(strings.Repeat("b", fingerprintKeyBytes)))
	testutil.FailErr(t, "construct second fingerprinter", err)

	const secret = "ghp_Kg5FiiXSE4tj3gDONnze6GMypjsxsCu09Aq3"
	got := first.Fingerprint(secret)
	if got == "" || got != first.Fingerprint(secret) {
		t.Fatal("fingerprint is empty or unstable")
	}
	if got == first.Fingerprint(secret+"x") || got == second.Fingerprint(secret) {
		t.Fatal("fingerprint did not bind exact bytes and device key")
	}
	if strings.Contains(string(got), secret) {
		t.Fatal("fingerprint contains the secret")
	}
}

func TestFingerprintKeyPersistsInPrivateCredentialStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), credentialstore.VaultBasename)
	slot := credentialstore.Slot{
		Path: path, Namespace: credentialstore.NamespaceSecretFingerprint, Context: fingerprintKeyContext,
	}
	firstStore := credentialstore.NewEmpty(slot, validFingerprintKeyID)
	first, err := loadOrCreateFingerprinter(firstStore)
	testutil.FailErr(t, "create fingerprint key", err)

	reloadedStore, err := credentialstore.OpenFile(slot, validFingerprintKeyID)
	testutil.FailErr(t, "reopen fingerprint key store", err)
	reloaded, err := loadOrCreateFingerprinter(reloadedStore)
	testutil.FailErr(t, "reload fingerprint key", err)
	if first.Fingerprint("same secret") != reloaded.Fingerprint("same secret") {
		t.Fatal("persisted key did not preserve fingerprint identity")
	}
	info, err := os.Stat(path)
	testutil.FailErr(t, "stat fingerprint key store", err)
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("fingerprint key store mode = %o, want 600", info.Mode().Perm())
	}
}

func TestMatcherReturnsFingerprintWithoutRetainingSecret(t *testing.T) {
	m, err := BuildMatcher(Bundled())
	testutil.FailErr(t, "build matcher", err)
	fingerprinter, err := NewFingerprinter([]byte(strings.Repeat("k", fingerprintKeyBytes)))
	testutil.FailErr(t, "construct fingerprinter", err)
	m.SetFingerprinter(fingerprinter)

	hits := m.Screen(plantAWS)
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	if hits[0].Fingerprint != fingerprinter.Fingerprint(plantAWS) {
		t.Fatalf("fingerprint = %q, want exact matched bytes", hits[0].Fingerprint)
	}
}
