package guidance_test

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/evidence"
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/projectroot"
	"github.com/lycaon/lycaon/pkg/api"
)

// M7: an empty ledger means nothing was observed, so a caller must have failed on a
// read error first. The union reports that error rather than an empty result.
func TestLedgerReadErrorSurfacesBeforeResolution(t *testing.T) {
	t.Parallel()
	want := errors.New("ledger unavailable")
	_, err := guidance.UnionCloseoutEvidence(context.Background(), failingEvidenceReader{err: want}, "session", nil)
	if !errors.Is(err, want) {
		t.Fatalf("err = %v", err)
	}
	got := onlyOutcome(t, "[read#1]", evidence.CitationRoots{}, evidence.Ledger{})
	if got.Outcome != guidance.CitationNotObserved {
		t.Fatalf("empty ledger = %+v", got)
	}
}

type failingEvidenceReader struct{ err error }

func (r failingEvidenceReader) LoadLedger(context.Context, string) (evidence.Ledger, error) {
	return evidence.Ledger{}, r.err
}

func (r failingEvidenceReader) WorkerLegs(context.Context, string, time.Time) ([]guidance.EvidenceLeg, error) {
	return nil, r.err
}

// H2, H3, H8: a coordinate needs line facts for that location, on any record.
func TestDraftCoordinatesAreChecked(t *testing.T) {
	t.Parallel()
	read := func(handle, path string, start, end int) evidence.Record {
		return evidence.Record{Handle: handle, Kind: "read", Shape: evidence.ShapeFileRegion, Path: path, LineRanges: []evidence.LineRange{{Start: start, End: end}}}
	}
	reads := evidence.AssembleLedger([]evidence.Record{read("read#1", "a.go", 1, 10)})
	survey := evidence.AssembleLedger([]evidence.Record{{Handle: "list#1", Kind: "list", Shape: evidence.ShapeFileRegion, Path: "pkg/doc.go", Survey: true}})
	both := evidence.AssembleLedger([]evidence.Record{{Handle: "list#1", Kind: "list", Shape: evidence.ShapeFileRegion, Path: "a.go", Survey: true}, read("read#1", "a.go", 1, 10)})
	gate := evidence.AssembleLedger([]evidence.Record{{Handle: "verify#1", Kind: "verify", GateType: "verify", Path: "g/h.go"}})
	zero := evidence.AssembleLedger([]evidence.Record{{Handle: "grep#1", Kind: "grep", Shape: evidence.ShapeOpaque, Path: "a.go"}})
	pathless := evidence.AssembleLedger([]evidence.Record{{Handle: "command#4", Kind: "command", Shape: evidence.ShapeCommand}, {Handle: "task#1", Kind: "task", Shape: evidence.ShapeOpaque}})
	cases := []struct {
		name, source string
		ev           evidence.Ledger
		want         guidance.CitationOutcome
		handle       string
	}{
		{"AC-AST-01 single line past the read", "[a.go:999]", reads, guidance.CitationUnverifiable, "read#1"},
		{"explicit handle past the read", "[read#1:999]", reads, guidance.CitationUnverifiable, "read#1"},
		{"handle and path past the read", "[read#1 a.go:999]", reads, guidance.CitationUnverifiable, "read#1"},
		{"one-line range past the read", "[a.go:99-99]", reads, guidance.CitationUnverifiable, "read#1"},
		{"line just past the read", "[a.go:11]", reads, guidance.CitationUnverifiable, "read#1"},
		{"workspace link past the read", "[x](a.go#L999)", reads, guidance.CitationUnverifiable, "read#1"},
		{"line inside the read", "[a.go:10]", reads, guidance.CitationGrounded, "read#1"},
		{"one-line range inside the read", "[a.go:4-4]", reads, guidance.CitationGrounded, "read#1"},
		{"AC-AST-13 survey range", "[pkg/doc.go:1-50]", survey, guidance.CitationUnverifiable, "list#1"},
		{"survey line", "[pkg/doc.go:7]", survey, guidance.CitationUnverifiable, "list#1"},
		{"survey handle range", "[list#1:1-50]", survey, guidance.CitationUnverifiable, "list#1"},
		{"survey bare", "[pkg/doc.go]", survey, guidance.CitationSurvey, ""},
		{"AC-AST-01 survey cannot exempt a range", "[a.go:3-40]", both, guidance.CitationUnverifiable, "list#1"},
		{"survey and read bare binds content", "[a.go]", both, guidance.CitationGrounded, "read#1"},
		{"survey and read line binds content", "[a.go:3]", both, guidance.CitationGrounded, "read#1"},
		{"AC-AST-13 gate line", "[g/h.go:3]", gate, guidance.CitationUnverifiable, "verify#1"},
		{"gate excerpt", `[g/h.go:3 "some meaningful excerpt"]`, gate, guidance.CitationUnverifiable, "verify#1"},
		{"gate handle line", "[verify#1:3]", gate, guidance.CitationUnverifiable, "verify#1"},
		{"gate bare handle", "[verify#1]", gate, guidance.CitationGrounded, "verify#1"},
		{"AC-AST-11 zero match line", "[grep#1:3]", zero, guidance.CitationUnverifiable, "grep#1"},
		{"zero match path line", "[a.go:3]", zero, guidance.CitationUnverifiable, "grep#1"},
		{"zero match bare", "[grep#1]", zero, guidance.CitationGrounded, "grep#1"},
		{"zero match bare path", "[a.go]", zero, guidance.CitationGrounded, "grep#1"},
		{"command line", "[command#4:3]", pathless, guidance.CitationUnverifiable, "command#4"},
		{"dispatch line", "[task#1:1]", pathless, guidance.CitationUnverifiable, "task#1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := onlyOutcome(t, tc.source, evidence.CitationRoots{}, tc.ev)
			if got.Outcome != tc.want || got.Resolution.Handle != tc.handle {
				t.Fatalf("outcome = %+v", got)
			}
			if tc.want == guidance.CitationUnverifiable && got.Code != guidance.SynthCitationUnverifiableCode {
				t.Fatalf("code = %q", got.Code)
			}
		})
	}
}

// C2: this exact line once spun forever in excerpt anchoring. Resolving through
// JSON, which the grammar's bound does not cover, shows the loop cannot wrap.
func TestLineNearIntegerLimitTerminates(t *testing.T) {
	t.Parallel()
	ev := ledgerOf(readMessages("a.go", lineTexts(1, 10)))
	report := guidance.CoordinatorCompletionReport{Synthesis: "x", CitedEvidence: []guidance.CoordinatorCitedEvidence{
		{Evidence: "read#1", Line: math.MaxInt - 2, Excerpt: "content of line number 3"},
	}}
	guidance.EvaluateCloseoutCitations(evidence.CitationRoots{}, "implement_synthesis", report, guidance.CloseoutEvidence{Ledger: ev})
}

// AC-AST-03: a leg may be named like a directive.
func TestDraftReservedNamespaces(t *testing.T) {
	t.Parallel()
	ev := evidence.NamespaceLedger(ledgerOf(readMessages("a.go", lineTexts(1, 9))), "verify")
	got := onlyOutcome(t, "[verify:read#1:2]", evidence.CitationRoots{}, ev)
	if got.Outcome != guidance.CitationGrounded || got.Resolution.Handle != "verify:read#1" {
		t.Fatalf("outcome = %+v", got)
	}
}

// The evidence catalog decides handle kinds, and the ledger adds any kind it holds.
func TestDraftHandleKinds(t *testing.T) {
	t.Parallel()
	ev := evidence.AssembleLedger([]evidence.Record{{Handle: "task#1", Kind: "task", Shape: evidence.ShapeOpaque}})
	known := guidance.KnownEvidenceKind(ev)
	for kind, want := range map[string]bool{"read": true, "command": true, "task": true, "pr": false, "section": false} {
		if known(kind) != want {
			t.Errorf("known(%q) = %v", kind, !want)
		}
	}
	namespaced := evidence.NamespaceLedger(evidence.AssembleLedger([]evidence.Record{{Handle: "custom#1", Shape: evidence.ShapeOpaque}}), "leg")
	if !guidance.KnownEvidenceKind(namespaced)("custom") {
		t.Fatal("a namespaced handle's kind is known")
	}
	if got := resolveMarkdown("[pr#123] and [section#intro] and [read#3]", evidence.CitationRoots{}, ev); len(got.Occurrences) != 1 || got.Occurrences[0].Outcome != guidance.CitationNotObserved {
		t.Fatalf("resolution = %+v", got)
	}
}

// M2: a schemeless www link names neither scheme.
func TestDraftWWWMatchesEitherScheme(t *testing.T) {
	t.Parallel()
	ev := evidence.AssembleLedger([]evidence.Record{
		{Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, URL: "https://www.a.test/p"},
		{Handle: "web#2", Kind: "web", Shape: evidence.ShapeURL, URL: "http://www.b.test/p"},
	})
	for source, want := range map[string]guidance.CitationOutcome{
		"www.a.test/p":             guidance.CitationGrounded,
		"www.b.test/p":             guidance.CitationGrounded,
		"[www.a.test/p]":           guidance.CitationGrounded,
		"www.c.test/p":             guidance.CitationNotObserved,
		"[x](http://www.a.test/p)": guidance.CitationNotObserved,
	} {
		if got := onlyOutcome(t, source, evidence.CitationRoots{}, ev); got.Outcome != want {
			t.Errorf("%s = %+v", source, got)
		}
	}
}

// M10, low: a root with no label binds by identity; an id that names no root
// resolves nowhere, even when it equals another root's label.
func TestDraftSourceLinkRoots(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	roots := evidence.CitationRoots{ActiveRootID: "r", Roots: []projectroot.RootRef{{ID: "r", Path: dir, IsPrimary: true}, {ID: "s", Label: "shared", Path: t.TempDir()}}}
	ev := evidence.AssembleLedger([]evidence.Record{
		{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "a.go", LineRanges: []evidence.LineRange{{Start: 1, End: 9}}},
		{Handle: "read#2", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "@shared/b.go", LineRanges: []evidence.LineRange{{Start: 1, End: 9}}},
	})
	if got := onlyOutcome(t, "[x](source://r/a.go#L1)", roots, ev); got.Outcome != guidance.CitationGrounded {
		t.Fatalf("unlabeled root = %+v", got)
	}
	if got := onlyOutcome(t, "[x](source://shared/b.go#L1)", roots, ev); got.Outcome != guidance.CitationNotObserved {
		t.Fatalf("a label is no root id: %+v", got)
	}
}

// B2: a mutation supports coordinates on its path, traced to it; the read it retired
// keeps its own outcome; a mutation that reports its lines is held to them.
func TestDraftCoordinatesAfterMutation(t *testing.T) {
	t.Parallel()
	retired := evidence.Record{Handle: "read#1", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "a.go", SupersededBy: "edit#1", LineRanges: []evidence.LineRange{{Start: 1, End: 100}}}
	edited := evidence.AssembleLedger([]evidence.Record{retired, {Handle: "edit#1", Kind: "edit", Shape: evidence.ShapeFileRegion, Path: "a.go"}})
	written := evidence.AssembleLedger([]evidence.Record{{Handle: "write#1", Kind: "write", Shape: evidence.ShapeFileRegion, Path: "n.go"}})
	observed := evidence.AssembleLedger([]evidence.Record{
		{Handle: "edit#1", Kind: "edit", Shape: evidence.ShapeFileRegion, Path: "a.go"},
		{Handle: "read#2", Kind: "read", Shape: evidence.ShapeFileRegion, Path: "a.go", LineRanges: []evidence.LineRange{{Start: 1, End: 10}}},
		{Handle: "write#3", Kind: "write", Shape: evidence.ShapeFileRegion, Path: "a.go"},
	})
	deleted := evidence.AssembleLedger([]evidence.Record{{Handle: "delete#1", Kind: "delete", Shape: evidence.ShapeFileRegion, Path: "gone.go"}})
	reported := evidence.AssembleLedger([]evidence.Record{{Handle: "edit#1", Kind: "edit", Shape: evidence.ShapeFileRegion, Path: "a.go", LineRanges: []evidence.LineRange{{Start: 40, End: 45}}}})
	cases := []struct {
		name, source string
		ev           evidence.Ledger
		want         guidance.CitationOutcome
		handle       string
		verdict      evidence.Verdict
	}{
		{"B2 line after an edit", "[a.go:42]", edited, guidance.CitationGrounded, "edit#1", evidence.VerdictTraced},
		{"range after an edit", "[a.go:42-43]", edited, guidance.CitationGrounded, "edit#1", evidence.VerdictTraced},
		{"link after an edit", "[x](a.go#L42)", edited, guidance.CitationGrounded, "edit#1", evidence.VerdictTraced},
		{"explicit edit line", "[edit#1:42]", edited, guidance.CitationGrounded, "edit#1", evidence.VerdictTraced},
		{"line after a write", "[n.go:1]", written, guidance.CitationGrounded, "write#1", evidence.VerdictTraced},
		{"the observing record outranks the mutation", "[a.go:2]", observed, guidance.CitationGrounded, "read#2", ""},
		{"past the read, a mutation still supports it", "[a.go:999]", observed, guidance.CitationGrounded, "edit#1", evidence.VerdictTraced},
		{"a deleted file has no line", "[gone.go:42]", deleted, guidance.CitationUnverifiable, "delete#1", ""},
		{"retired read keeps its outcome", "[read#1:42]", edited, guidance.CitationGrounded, "read#1", ""},
		{"retired read past its lines", "[read#1:200]", edited, guidance.CitationUnverifiable, "read#1", ""},
		{"reported lines inside", "[a.go:41]", reported, guidance.CitationGrounded, "edit#1", ""},
		{"reported lines outside", "[a.go:50]", reported, guidance.CitationUnverifiable, "edit#1", ""},
		{"another path is not the edit's", "[b.go:1]", edited, guidance.CitationNotObserved, "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := onlyOutcome(t, tc.source, evidence.CitationRoots{}, tc.ev)
			if got.Outcome != tc.want || got.Resolution.Handle != tc.handle {
				t.Fatalf("outcome = %+v", got)
			}
			if tc.verdict != "" && got.Resolution.Verdict != tc.verdict {
				t.Fatalf("verdict = %s", got.Resolution.Verdict)
			}
		})
	}
}

// The panel shows matched as an exact check, so a coordinate nobody compared stays
// traced: a mutation supports any line on its path, and no line was read.
func TestCitedEvidenceWireVerdicts(t *testing.T) {
	t.Parallel()
	edited := evidence.AssembleLedger([]evidence.Record{{Handle: "edit#1", Kind: "edit", Shape: evidence.ShapeFileRegion, Path: "a.go"}})
	read := handleMembershipLedger(t).Ledger
	cases := []struct {
		name    string
		ev      evidence.Ledger
		cite    guidance.CoordinatorCitedEvidence
		handle  string
		verdict api.CitationVerdict
		end     int
	}{
		{name: "line on a mutated path", ev: edited, cite: guidance.CoordinatorCitedEvidence{Path: "a.go", Line: 42}, handle: "edit#1", verdict: api.CitationVerdictTraced},
		{name: "fabricated line on a mutated path", ev: edited, cite: guidance.CoordinatorCitedEvidence{Path: "a.go", Line: 999999}, handle: "edit#1", verdict: api.CitationVerdictTraced},
		{name: "range on a mutated path keeps its end", ev: edited, cite: guidance.CoordinatorCitedEvidence{Path: "a.go", Line: 42, EndLine: 43}, handle: "edit#1", verdict: api.CitationVerdictTraced, end: 43},
		{name: "explicit mutation handle", ev: edited, cite: guidance.CoordinatorCitedEvidence{Evidence: "edit#1", Line: 42}, handle: "edit#1", verdict: api.CitationVerdictTraced},
		{name: "read line with no excerpt", ev: read, cite: guidance.CoordinatorCitedEvidence{Path: "src/a.go", Line: 2}, handle: "read#1", verdict: api.CitationVerdictTraced},
		{name: "bare handle", ev: read, cite: guidance.CoordinatorCitedEvidence{Evidence: "read#1"}, handle: "read#1", verdict: api.CitationVerdictTraced},
		{name: "bare path", ev: read, cite: guidance.CoordinatorCitedEvidence{Path: "src/a.go"}, handle: "read#1", verdict: api.CitationVerdictTraced},
		{name: "excerpt the read holds", ev: read, cite: guidance.CoordinatorCitedEvidence{Path: "src/a.go", Line: 1, Excerpt: "package main"}, handle: "read#1", verdict: api.CitationVerdictMatched},
		{name: "a line the read did not cover never reaches the panel", ev: read, cite: guidance.CoordinatorCitedEvidence{Path: "src/a.go", Line: 999}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := guidance.ResolveCitedEvidenceForWire(evidence.CitationRoots{}, tc.ev, []guidance.CoordinatorCitedEvidence{tc.cite})
			if tc.handle == "" {
				if len(got) != 0 {
					t.Fatalf("wire = %+v, want nothing", got)
				}
				return
			}
			if len(got) != 1 || got[0].Handle != tc.handle || got[0].Verdict != tc.verdict || got[0].EndLine != tc.end {
				t.Fatalf("wire = %+v", got)
			}
		})
	}
}

// N1: the observed scheme survives on an accepted www link.
func TestPresentWWWKeepsObservedScheme(t *testing.T) {
	t.Parallel()
	ev := evidence.AssembleLedger([]evidence.Record{{Handle: "web#1", Kind: "web", Shape: evidence.ShapeURL, URL: "https://www.a.test/p"}})
	roots := evidence.CitationRoots{}
	for source, want := range map[string]string{
		"see www.a.test/p now":   "see <https://www.a.test/p> now",
		"[www.a.test/p]":         "[<https://www.a.test/p>]",
		"[see www.a.test/p]":     "[see <https://www.a.test/p>]",
		"see www.other.test now": `see www\.other.test now`,
	} {
		draft := guidance.ParseCloseoutDraft(source, guidance.DraftParseOptions(roots, ev))
		resolved := guidance.ResolveDraftCitations(roots, "implement_synthesis", draft, guidance.CloseoutEvidence{Ledger: ev}, nil)
		if got := guidance.PresentCloseoutMarkdown(draft, resolved); got != want {
			t.Errorf("%s = %q, want %q", source, got, want)
		}
	}
}

// B4: the ledger's kinds are read once, not for each bracket.
func TestKnownEvidenceKindIsPrecomputed(t *testing.T) {
	t.Parallel()
	ev := evidence.AssembleLedger([]evidence.Record{{Handle: "custom#1", Kind: "custom", Shape: evidence.ShapeOpaque}})
	known := guidance.KnownEvidenceKind(ev)
	clear(ev.Handles)
	if !known("custom") {
		t.Fatal("the set was not taken at construction")
	}
}
