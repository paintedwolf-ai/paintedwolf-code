package evidence_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/evidence"
)

// Decision 056 invariant: a BodyUnavailable record stays in Handles (keeping the
// ledger non-empty and ordinals monotonic) while never grounding any citation
// through handle, path, coordinate, excerpt, URL, or artifact paths, and never
// blocking a new healthy observation of the same file from grounding.
func TestBodyUnavailableRecordNeverGroundsAndRecoversWithNewHandle(t *testing.T) {
	t.Parallel()

	damagedFile := evidence.Record{
		Handle:          "read#1",
		Kind:            "read",
		Shape:           evidence.ShapeFileRegion,
		Fidelity:        "structured",
		SourceTool:      "read",
		Path:            "internal/app.go",
		LineRanges:      []evidence.LineRange{{Start: 1, End: 10}},
		BodyUnavailable: true,
	}
	damagedURL := evidence.Record{
		Handle:          "fetch#1",
		Kind:            "fetch",
		Shape:           evidence.ShapeURL,
		Fidelity:        "structured",
		SourceTool:      "fetch_url",
		URL:             "https://example.test/doc",
		BodyUnavailable: true,
	}
	damagedArtifact := evidence.Record{
		Handle:          "artifact#1",
		Kind:            "artifact",
		Shape:           evidence.ShapeArtifact,
		Fidelity:        "structured",
		SourceTool:      "capture",
		Path:            "shot.png",
		ArtifactID:      "art-1",
		BodyUnavailable: true,
	}

	ev := evidence.AssembleLedger([]evidence.Record{damagedFile, damagedURL, damagedArtifact})

	// Ledger stays non-empty (exemption rule preserved).
	if len(ev.Handles) != 3 {
		t.Fatalf("len(ev.Handles) = %d, want 3", len(ev.Handles))
	}
	if got := ev.UnavailableBodyHandles(); len(got) != 3 {
		t.Fatalf("UnavailableBodyHandles = %v, want 3 handles", got)
	}

	// Explicit handle resolution: bare, coordinate, path+coord, excerpt.
	for _, tc := range []struct {
		name   string
		handle string
		triple evidence.Triple
	}{
		{"bare_handle", "read#1", evidence.Triple{}},
		{"handle_coord", "read#1", evidence.Triple{Line: 3}},
		{"handle_path_coord", "read#1", evidence.Triple{Path: "internal/app.go", Line: 3}},
		{"handle_excerpt", "read#1", evidence.Triple{Path: "internal/app.go", Line: 3, Excerpt: "return 1"}},
		{"artifact_handle", "artifact#1", evidence.Triple{Path: "shot.png"}},
	} {
		res := evidence.ResolveObservation(evidence.CitationRoots{}, ev, tc.handle, tc.triple)
		if res.Verdict != evidence.VerdictUnverifiable || res.Reason != evidence.VerifyReasonBodyUnavailable {
			t.Errorf("%s: ResolveObservation = %+v, want unverifiable/%s", tc.name, res, evidence.VerifyReasonBodyUnavailable)
		}
	}

	// Path-based resolution: bare path, coord, coord+excerpt, with handle hint.
	for _, tc := range []struct {
		name   string
		triple evidence.Triple
		hint   string
	}{
		{"bare_path", evidence.Triple{Path: "internal/app.go"}, ""},
		{"path_coord", evidence.Triple{Path: "internal/app.go", Line: 3}, ""},
		{"path_coord_excerpt", evidence.Triple{Path: "internal/app.go", Line: 3, Excerpt: "return 1"}, ""},
		{"path_coord_hint", evidence.Triple{Path: "internal/app.go", Line: 3}, "read#1"},
	} {
		res := evidence.Resolve(evidence.CitationRoots{}, tc.triple, ev, tc.hint)
		if res.Verdict != evidence.VerdictUnverifiable {
			t.Errorf("%s: Resolve = %+v, want unverifiable", tc.name, res)
		}
	}

	// URL observation must not ground from a damaged record.
	if urls := evidence.ObservedURLsFromEvidence(ev); len(urls) != 0 {
		t.Fatalf("ObservedURLsFromEvidence = %v, want empty", urls)
	}
	if ok, reason := evidence.VerifyHandle(ev, "fetch#1", evidence.Claim{URL: "https://example.test/doc"}); ok || reason != evidence.VerifyReasonBodyUnavailable {
		t.Fatalf("VerifyHandle(fetch#1) = (%v, %q), want (false, %q)", ok, reason, evidence.VerifyReasonBodyUnavailable)
	}

	// Recovery: re-observing the same file with a new healthy handle grounds cleanly.
	healthy := evidence.Record{
		Handle:     "read#2",
		Kind:       "read",
		Shape:      evidence.ShapeFileRegion,
		Fidelity:   "structured",
		SourceTool: "read",
		Path:       "internal/app.go",
		LineRanges: []evidence.LineRange{{Start: 1, End: 10}},
		Body:       []string{"1| package app", "2| ", "3| func Val() int { return 1 }"},
	}
	evRecovered := evidence.AssembleLedger([]evidence.Record{damagedFile, healthy})
	if res := evidence.Resolve(evidence.CitationRoots{}, evidence.Triple{Path: "internal/app.go", Line: 3, Excerpt: "return 1"}, evRecovered, ""); res.Verdict != evidence.VerdictMatched || res.Handle != "read#2" {
		t.Fatalf("Resolve after recovery = %+v, want matched/read#2", res)
	}
	// The damaged read#1 still never grounds when cited explicitly.
	if res := evidence.ResolveObservation(evidence.CitationRoots{}, evRecovered, "read#1", evidence.Triple{Line: 3}); res.Verdict != evidence.VerdictUnverifiable || res.Reason != evidence.VerifyReasonBodyUnavailable {
		t.Fatalf("ResolveObservation(read#1) after recovery = %+v, want unverifiable/%s", res, evidence.VerifyReasonBodyUnavailable)
	}
}
