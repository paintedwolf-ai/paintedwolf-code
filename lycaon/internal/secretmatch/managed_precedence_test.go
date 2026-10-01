package secretmatch_test

import (
	"context"
	"testing"

	"github.com/lycaon/lycaon/internal/secretmatch"
)

// A managed capability names its bytes even when a container read also holds
// them, so the span reads as tracked and never invites a second capability.
func TestManagedEvidenceNamesSharedBytes(t *testing.T) {
	const secret = "Pw4-generated-value-9"
	const reference = "{{paintedwolf-secret:6f0c7f3e-0d59-4a55-9d64-1b2a3c4d5e6f}}"
	m := secretmatch.NewInertMatcher()
	m.SetHarvestSource(func(context.Context) []secretmatch.HarvestedValue {
		return []secretmatch.HarvestedValue{
			{Name: "POSTGRES_PASSWORD", Container: ".env", Secret: secret, Fingerprint: "fp",
				RuleID: secretmatch.HarvestRuleID, Title: "A value from .env", Source: secretmatch.SourceContainerHarvest},
			{Name: "db password", Secret: secret, Fingerprint: "fp", RuleID: secretmatch.ManagedRuleID,
				Title: secretmatch.ManagedRuleTitle, Source: secretmatch.SourceRememberedMatch,
				Reference: reference, NonDisclosable: true},
		}
	})
	hits := m.ScreenContext(context.Background(), "POSTGRES_PASSWORD="+secret)
	if len(hits) != 1 {
		t.Fatalf("hits = %+v", hits)
	}
	if hits[0].RuleID != secretmatch.ManagedRuleID || hits[0].Reference != reference {
		t.Fatalf("container evidence named a managed value: %+v", hits[0])
	}
}
