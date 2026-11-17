package guidance_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

// A shape the host has no code for reaches the wire unchanged, so the client
// can facet on it.
func TestStampEvidenceRecordsCarriesNovelShapes(t *testing.T) {
	ev := evidence.Ledger{Handles: map[string]evidence.Record{
		"read#1":  {Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "src/main.go"},
		"novel#1": {Handle: "novel#1", Kind: "novel", Shape: "custom_timeline", Body: []string{"event stream payload"}},
	}}
	var grounding api.CitationGrounding
	guidance.StampEvidenceRecords(&grounding, ev)
	got := map[string]string{}
	for _, rec := range grounding.EvidenceRecords {
		got[rec.Handle] = rec.Shape
	}
	want := map[string]string{"read#1": evidence.ShapeFileRegion, "novel#1": "custom_timeline"}
	if len(got) != len(want) || got["read#1"] != want["read#1"] || got["novel#1"] != want["novel#1"] {
		t.Fatalf("wire shapes = %v want %v", got, want)
	}
}

// Internal resolver verdicts map onto the citation wire enum; a host-bound
// citation surfaces as matched.
func TestWireCitationVerdictMapsEveryGroundedOrRejectedVerdict(t *testing.T) {
	for in, want := range map[evidence.Verdict]api.CitationVerdict{
		evidence.VerdictMatched:      api.CitationVerdictMatched,
		evidence.VerdictTraced:       api.CitationVerdictTraced,
		evidence.VerdictUnverifiable: api.CitationVerdictUnverifiable,
		evidence.VerdictBound:        api.CitationVerdictMatched,
	} {
		if got := guidance.WireCitationVerdict(in); got != want {
			t.Errorf("WireCitationVerdict(%q) = %q want %q", in, got, want)
		}
	}
}
