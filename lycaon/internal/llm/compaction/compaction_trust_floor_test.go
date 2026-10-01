package compaction

import (
	"testing"

	"github.com/lycaon/lycaon/pkg/api"
)

func ctxMsg(id string, tier api.ContentTrustTier, partTiers ...api.ContentTrustTier) ContextMessage {
	msg := ContextMessage{ID: id, TrustTier: tier}
	for _, t := range partTiers {
		msg.ContentParts = append(msg.ContentParts, api.MessageContentPart{TrustTier: t})
	}
	return msg
}

// TestCompactedTrustFloorInheritsWhatItReplaced preserves untrusted source tiers.
func TestCompactedTrustFloorInheritsWhatItReplaced(t *testing.T) {
	t.Parallel()
	before := []ContextMessage{
		ctxMsg("m1", api.ContentTrustTierTrusted),
		ctxMsg("m2", api.ContentTrustTierUntrusted),
		ctxMsg("m3", api.ContentTrustTierTrusted),
	}
	tail := []ContextMessage{before[2]}
	if got := compactedTrustFloor(before, tail); got != api.ContentTrustTierUntrusted {
		t.Fatalf("floor = %q, want untrusted", got)
	}
}

// TestCompactedTrustFloorReadsParts includes untrusted message parts.
func TestCompactedTrustFloorReadsParts(t *testing.T) {
	t.Parallel()
	before := []ContextMessage{
		ctxMsg("m1", api.ContentTrustTierTrusted, api.ContentTrustTierUntrusted),
	}
	if got := compactedTrustFloor(before, nil); got != api.ContentTrustTierUntrusted {
		t.Fatalf("floor = %q, want untrusted", got)
	}
}

// TestCompactedTrustFloorIgnoresKeptMessages excludes tail messages from the summary.
func TestCompactedTrustFloorIgnoresKeptMessages(t *testing.T) {
	t.Parallel()
	untrusted := ctxMsg("m2", api.ContentTrustTierUntrusted)
	before := []ContextMessage{ctxMsg("m1", api.ContentTrustTierTrusted), untrusted}
	if got := compactedTrustFloor(before, []ContextMessage{untrusted}); got != api.ContentTrustTierTrusted {
		t.Fatalf("floor = %q, want trusted — the untrusted message was kept, not replaced", got)
	}
}

// A message with no id cannot be matched against the tail, so it counts as
// replaced. That is the conservative direction: it can only lower the floor.
func TestCompactedTrustFloorCountsUnidentifiedMessagesAsReplaced(t *testing.T) {
	t.Parallel()
	before := []ContextMessage{ctxMsg("", api.ContentTrustTierUntrusted)}
	if got := compactedTrustFloor(before, before); got != api.ContentTrustTierUntrusted {
		t.Fatalf("floor = %q, want untrusted", got)
	}
}

// Nothing replaced means the summary restates nothing and stands on its own
// authorship.
func TestCompactedTrustFloorWithNothingReplaced(t *testing.T) {
	t.Parallel()
	kept := ctxMsg("m1", api.ContentTrustTierUntrusted)
	if got := compactedTrustFloor([]ContextMessage{kept}, []ContextMessage{kept}); got != api.ContentTrustTierTrusted {
		t.Fatalf("floor = %q, want trusted", got)
	}
}
