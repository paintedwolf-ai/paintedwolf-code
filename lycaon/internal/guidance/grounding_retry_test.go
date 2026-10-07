package guidance

import "testing"

func TestGroundingRetryBypassesStuckDetection(t *testing.T) {
	t.Parallel()

	for _, code := range []string{
		InvestURLNotObservedCode,
		SynthURLNotObservedCode,
		WorkerURLNotObservedCode,
		ReportFenceUnreadableCode,
		ReportDocumentInvalidCode,
		ReportClaimUnreportedCode,
		ReportInventoryUnaccountedCode,
	} {
		if !GroundingRetryBypassesStuckDetection(code) {
			t.Fatalf("code %q should bypass stuck detection", code)
		}
	}
	if GroundingRetryBypassesStuckDetection(InvestHandleNotObservedCode) {
		t.Fatal("handle code must not bypass stuck detection")
	}
}

// The citations-required family keys on Code alone, so a drifting observed-path count
// still trips the same-offender breaker. A handle-code reject keeps the count in its key.
func TestGroundingOffenderKeyCitationsRequiredCodeOnly(t *testing.T) {
	t.Parallel()

	for _, code := range []string{InvestCitationsRequiredCode, SynthCitationsRequiredCode} {
		a := GroundingOffenderKey(code, map[string]any{"offender_count": 106, "offenders_sample": "a.go, b.go"})
		b := GroundingOffenderKey(code, map[string]any{"offender_count": 94, "offenders_sample": "c.go"})
		if a != b || a != code {
			t.Fatalf("%s: keys should both equal the Code, got %q and %q", code, a, b)
		}
	}

	handleA := GroundingOffenderKey(InvestHandleNotObservedCode, map[string]any{"offender_count": 3, "offenders_sample": "x"})
	handleB := GroundingOffenderKey(InvestHandleNotObservedCode, map[string]any{"offender_count": 4, "offenders_sample": "y"})
	if handleA == handleB {
		t.Fatal("handle code must retain offender payload in its key (counts distinguish rejects)")
	}
}
