package guidance

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
)

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

func TestGroundingOffenderKeyPreservesHandleAcrossForms(t *testing.T) {
	t.Parallel()

	ev := CloseoutEvidence{Ledger: evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "observed.go", LineRanges: []evidence.LineRange{{Start: 1, End: 5}}},
	})}
	opts := DraftParseOptions(evidence.CitationRoots{}, ev.Ledger)

	cases := []struct {
		name         string
		citation     string
		wantOffender string
	}{
		{"bare handle A", "[read#99]", "read#99"},
		{"bare handle B", "[read#88]", "read#88"},
		{"handle plus line A", "[read#99:3]", "read#99:3"},
		{"handle plus line B", "[read#88:3]", "read#88:3"},
		{"handle plus range", "[read#99:3-5]", "read#99:3-5"},
		{"worker-prefixed handle plus line A", "[worker-1:read#1:3]", "worker-1:read#1:3"},
		{"worker-prefixed handle plus line B", "[worker-2:read#1:3]", "worker-2:read#1:3"},
		{"uuid worker-prefixed handle plus line", "[550e8400-e29b-41d4-a716-446655440000:read#1:3]", "550e8400-e29b-41d4-a716-446655440000:read#1:3"},
		{"path plus line A", "[missing.go:3]", "missing.go:3"},
		{"path plus line B", "[other.go:3]", "other.go:3"},
	}

	seenKeys := make(map[string]string, len(cases))
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			draft := ParseCloseoutDraft("Finding "+tc.citation+".", opts)
			resolved := ResolveDraftCitations(evidence.CitationRoots{}, "implement_investigate", draft, ev, nil)
			eval := EvaluateDraftCitations(draft, resolved)
			if eval.Code != InvestHandleNotObservedCode {
				t.Fatalf("code = %q, want %q (eval=%+v)", eval.Code, InvestHandleNotObservedCode, eval)
			}
			if len(eval.Offenders) != 1 || eval.Offenders[0] != tc.wantOffender {
				t.Fatalf("offenders = %v, want [%s]", eval.Offenders, tc.wantOffender)
			}
			key := GroundingOffenderKey(eval.Code, OffenderHintData(eval.Offenders))
			if prev, dup := seenKeys[key]; dup {
				t.Fatalf("key %q collided between %q and %q", key, prev, tc.name)
			}
			seenKeys[key] = tc.name
		})
	}

	// Direct formatter fallback when Resolution.Handle is empty but finding carries the handle.
	if got := FormatResolutionOffender(evidence.Resolution{Line: 3, EndLine: 3}, WorkerFindingInput{Evidence: "worker-1:read#1", Line: 3}); got != "worker-1:read#1:3" {
		t.Fatalf("FormatResolutionOffender fallback = %q, want %q", got, "worker-1:read#1:3")
	}
}
