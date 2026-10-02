package presence

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/credentialstore"
	"github.com/lycaon/lycaon/internal/testutil"
)

func testLedger(t *testing.T) *ReleaseLedger {
	t.Helper()
	store, err := credentialstore.OpenFile(credentialstore.Slot{
		Path: filepath.Join(t.TempDir(), credentialstore.VaultBasename), Namespace: credentialstore.NamespacePresenceReleases,
		Context: ledgerContext,
	}, validGrantID)
	testutil.FailErr(t, "open ledger store", err)
	return NewReleaseLedger(store)
}

func testCoverage() (ReleaseCoverage, Attestation) {
	expires := time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)
	return ReleaseCoverage{
			GrantID: "grant-1", Scope: "project", ProjectID: "project-1", ExpiresAt: &expires,
			Fingerprints: []string{"sf1_b", "sf1_a"}, RecipientDigest: "recipients",
		}, Attestation{
			ID: "11111111-1111-4111-8111-111111111111", PersonID: "owner", Authenticator: AuthenticatorMacOS,
			AttestedAt: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC),
		}
}

func TestLedgerCoversExactlyTheRecordedGrant(t *testing.T) {
	ledger := testLedger(t)
	ledger.now = func() time.Time { return time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC) }
	coverage, attestation := testCoverage()
	testutil.FailErr(t, "record", ledger.Record(coverage, attestation))
	if !ledger.Covers(coverage, attestation) {
		t.Fatal("ledger refused the grant it recorded")
	}
	reordered := coverage
	reordered.Fingerprints = []string{"sf1_a", "sf1_b"}
	if !ledger.Covers(reordered, attestation) {
		t.Fatal("ledger depended on fingerprint order")
	}
	widened := coverage
	widened.Fingerprints = append([]string{"sf1_c"}, coverage.Fingerprints...)
	extended := coverage
	later := coverage.ExpiresAt.Add(24 * time.Hour)
	extended.ExpiresAt = &later
	redirected := coverage
	redirected.RecipientDigest = "other recipients"
	forged := attestation
	forged.PersonID = "someone-else"
	for name, check := range map[string]bool{
		"widened fingerprints": ledger.Covers(widened, attestation),
		"extended expiry":      ledger.Covers(extended, attestation),
		"other recipients":     ledger.Covers(redirected, attestation),
		"forged attestation":   ledger.Covers(coverage, forged),
	} {
		if check {
			t.Errorf("ledger covered a grant with %s", name)
		}
	}
}

// A revoked grant restored from a stored copy never covers again.
func TestLedgerForgetsARevokedGrant(t *testing.T) {
	ledger := testLedger(t)
	ledger.now = func() time.Time { return time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC) }
	coverage, attestation := testCoverage()
	testutil.FailErr(t, "record", ledger.Record(coverage, attestation))
	testutil.FailErr(t, "forget", ledger.Forget(coverage.GrantID))
	if ledger.Covers(coverage, attestation) {
		t.Fatal("ledger covered a forgotten grant")
	}
}

func TestLedgerStopsCoveringAtExpiry(t *testing.T) {
	ledger := testLedger(t)
	coverage, attestation := testCoverage()
	ledger.now = func() time.Time { return time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC) }
	testutil.FailErr(t, "record", ledger.Record(coverage, attestation))
	ledger.now = func() time.Time { return *coverage.ExpiresAt }
	if ledger.Covers(coverage, attestation) {
		t.Fatal("ledger covered an expired grant")
	}
}
