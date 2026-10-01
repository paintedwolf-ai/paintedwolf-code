package secretmatch

import "testing"

func TestProvenanceFiltersOnlyContainedWireShapes(t *testing.T) {
	text := "λ before encoded-value after other-secret"
	matches := []Match{
		{Start: 9, End: 21, Fingerprint: "encoded-alias", Source: SourceShapeRule},
		{Start: 28, End: 40, Fingerprint: "unrelated", Source: SourceShapeRule},
	}
	got := OutsideProtected(text, matches, []string{"encoded-value"})
	if len(got) != 1 || got[0].Fingerprint != "unrelated" {
		t.Fatal("provenance removed unrelated wire evidence")
	}
}

func TestProvenancePreservesOverlappingExactIdentities(t *testing.T) {
	matches := []Match{
		{Start: 6, End: 10, Fingerprint: "short-secret", Source: SourceRememberedMatch},
		{Start: 0, End: 16, Fingerprint: "long-secret", Source: SourceRememberedMatch},
		{Start: 0, End: 16, Fingerprint: "long-shape", Source: SourceShapeRule},
	}
	got := OutsideProtected("prefix1234suffix", matches, []string{"1234"})
	if len(got) != 3 || got[1].Fingerprint != "long-secret" || got[2].Fingerprint != "long-shape" {
		t.Fatal("provenance suppressed another known credential")
	}
}

func TestEvidenceMergeKeepsOriginalIdentity(t *testing.T) {
	known := Match{Fingerprint: "same-value", RuleID: ManagedRuleID}
	got := MergeEvidence([]Match{known}, []Match{{Fingerprint: "same-value", RuleID: "catalog"}, {Fingerprint: "other-value", RuleID: "catalog"}})
	if len(got) != 2 || got[0].RuleID != ManagedRuleID || got[1].Fingerprint != "other-value" {
		t.Fatal("evidence merge changed the release set")
	}
}
