package secretmatch_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// A replacement may store bytes an earlier version held. The live version
// names them, whichever order the evidence arrives in.
func TestLiveVersionNamesBytesARetiredVersionShares(t *testing.T) {
	const secret = "Pw4-rotated-back-value-7"
	const reference = "{{paintedwolf-secret:0b8e1c52-6a0f-4a7e-9d3b-2c4e5f6a7b8c}}"
	retired := secretmatch.HarvestedValue{Name: "relay", Secret: secret, Fingerprint: "fp",
		RuleID: secretmatch.ManagedRuleID, Title: secretmatch.ManagedRuleTitle,
		Source: secretmatch.SourceRememberedMatch, Retired: true, NonDisclosable: true}
	live := retired
	live.Retired, live.Reference = false, reference
	for _, order := range [][]secretmatch.HarvestedValue{{retired, live}, {live, retired}} {
		m := secretmatch.NewInertMatcher()
		m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue { return order })
		hits := m.ScreenContext(context.Background(), "RELAY="+secret)
		if len(hits) != 1 {
			t.Fatalf("hits = %+v", hits)
		}
		if hits[0].Retired || hits[0].Reference != reference {
			t.Fatalf("retired evidence named live bytes: %+v", hits[0])
		}
	}
}

// Retired bytes keep no reference, even where a live capability's span
// overlaps them and the longer retired span names the range.
func TestRetiredEvidenceNeverBorrowsAReference(t *testing.T) {
	const retiredSecret = "Pw4-retired-longer-overlap"
	const liveSecret = "overlap-live-9x"
	const reference = "{{paintedwolf-secret:5d2a9c7e-1b3f-4e8a-a6c0-9f8e7d6c5b4a}}"
	m := secretmatch.NewInertMatcher()
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{
			{Name: "old", Secret: retiredSecret, Fingerprint: "old", RuleID: secretmatch.ManagedRuleID,
				Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch,
				Retired: true, NonDisclosable: true},
			{Name: "new", Secret: liveSecret, Fingerprint: "new", RuleID: secretmatch.ManagedRuleID,
				Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch,
				Reference: reference, NonDisclosable: true},
		}
	})
	hits := m.ScreenContext(context.Background(), "K=Pw4-retired-longer-overlap-live-9x")
	if len(hits) != 1 || !hits[0].Retired {
		t.Fatalf("hits = %+v, want the longer retired span", hits)
	}
	if hits[0].Reference != "" {
		t.Fatalf("retired hit borrowed a reference: %+v", hits[0])
	}
}
