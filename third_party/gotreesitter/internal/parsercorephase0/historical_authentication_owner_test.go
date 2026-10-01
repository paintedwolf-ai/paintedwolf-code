package parsercorephase0

import (
	"strings"
	"testing"
)

func TestHistoricalCertificateAuthenticationOwnedValidatesOwner(t *testing.T) {
	compact, _, _ := newSchedulerTransactionShiftFixture(t)
	if err := compact.SetHistoricalCertificateAuthenticationOwned(SchedulerTransactionToken{}, true); err == nil {
		t.Fatal("zero token enabled historical authentication")
	}
	var stale SchedulerTransactionToken
	if err := compact.RunFreshSchedulerSession(func(owner SchedulerTransactionToken) error {
		stale = owner
		if err := compact.SetHistoricalCertificateAuthenticationOwned(owner, true); err != nil {
			return err
		}
		if !compact.historicalCertificateAuthentication {
			t.Fatal("owned setter did not enable historical authentication")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if compact.historicalCertificateAuthentication {
		t.Fatal("fresh session retained historical authentication after exit")
	}
	if err := compact.SetHistoricalCertificateAuthenticationOwned(stale, false); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale token result=%v", err)
	}

	foreign, _, _ := newSchedulerTransactionShiftFixture(t)
	if err := foreign.RunFreshSchedulerSession(func(owner SchedulerTransactionToken) error {
		if err := compact.SetHistoricalCertificateAuthenticationOwned(owner, true); err == nil || !strings.Contains(err.Error(), "different core") {
			t.Fatalf("foreign token result=%v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := compact.Reset(); err != nil {
		t.Fatal(err)
	}
	if compact.historicalCertificateAuthentication {
		t.Fatal("Reset retained historical authentication")
	}
}
