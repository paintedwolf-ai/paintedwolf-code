package main

import (
	"testing"

	"github.com/lycaon/lycaon/internal/approvals"
)

func TestEntriesCarryTheirKeyAndTier(t *testing.T) {
	for _, key := range approvals.GateableKeys() {
		entry := enrichEntry(key, defaultEntry(key))
		if entry.Tool != key {
			t.Errorf("%s: tool %q", key, entry.Tool)
		}
		if spec, ok := entrySpecs[key]; ok && entry.TierHint != spec.tier {
			t.Errorf("%s: tier %q, want %q", key, entry.TierHint, spec.tier)
		}
	}
	const unknown = "not_a_gateable_key"
	if entry := defaultEntry(unknown); entry.Tool != unknown || entry.TierHint != entrySpecs[approvals.FallbackExplanationKey].tier {
		t.Fatalf("unknown key entry %+v", entry)
	}
}
