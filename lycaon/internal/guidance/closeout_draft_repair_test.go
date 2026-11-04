package guidance

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/citationmd"
	"github.com/lycaon/lycaon/internal/verification"
)

func repairOptions() citationmd.Options {
	return citationmd.Options{KnownPath: func(name string) bool { return name == "LICENSE" }}
}

// retainedFor is a cycle refused once on draft.
func retainedFor(content string, reportPhase bool, forcedBy ...string) RetainedCloseout {
	retained := RetainedCloseout{Active: true, ForcedBy: forcedBy}
	retained.Retain(ReadCloseoutReport(content, RetainedCloseout{}, reportPhase, repairOptions()).Draft)
	return retained
}

func repair(retained RetainedCloseout, raw string, reportPhase bool) CloseoutRead {
	return ReadCloseoutReport(raw, retained, reportPhase, repairOptions())
}

func TestCitationRepairDirectives(t *testing.T) {
	const url = "https://example.test/observed-page"
	for _, tc := range []struct {
		name, draft, raw, want string
		check                  func(t *testing.T, draft CloseoutDraft)
	}{
		{
			name: "AC-CLO-06 a handle repair changes the occurrence and metadata, never the narrative",
			draft: "The timeout moved [read#9:42] to the caller.", raw: "[cite: c1 => read#5:42]",
			want: "The timeout moved [read#5:42] to the caller.",
			check: func(t *testing.T, draft CloseoutDraft) {
				if len(draft.CitedEvidence) != 1 || draft.CitedEvidence[0].Evidence != "read#5" || draft.CitedEvidence[0].Line != 42 {
					t.Fatalf("metadata = %+v", draft.CitedEvidence)
				}
			},
		},
		{
			name: "AC-CLO-07 a repeated handle repairs only the targeted occurrence",
			draft: "One [read#9:1], two [read#9:2], three [read#9:3].", raw: "[cite: c2 => read#5:20]",
			want: "One [read#9:1], two [read#5:20], three [read#9:3].",
		},
		{
			name: "AC-CLO-07 group members are separate occurrences",
			draft: "See [read#9:1, grep#2:3].", raw: "[cite: c2 => read#5:20-22]",
			want: "See [read#9:1, read#5:20-22].",
		},
		{
			name: "AC-CLO-07 several directives apply together",
			draft: "One [read#9:1], two [read#9:2].", raw: "[cite: c2 => read#5:2]\n[cite: c1 => read#5:1]",
			want: "One [read#5:1], two [read#5:2].",
		},
		{
			name: "a bare path value stays a citation though unobserved",
			draft: "One [read#9:1].", raw: "[cite: c1 => internal/a.go:4]",
			want: "One [internal/a.go:4].",
		},
		{
			name: "AC-CLO-08 a URL repair keeps the label",
			draft: "Per [the RFC](https://example.test/old), done.", raw: "[cite: c1 => <" + url + ">]",
			want: "Per [the RFC](" + url + "), done.",
			check: func(t *testing.T, draft CloseoutDraft) {
				if len(draft.CitedURLs) != 1 || draft.CitedURLs[0] != url {
					t.Fatalf("urls = %v", draft.CitedURLs)
				}
			},
		},
		{
			name: "AC-CLO-08 an autolink repair",
			draft: "Per <https://example.test/old>, done.", raw: "[cite: c1 => <" + url + ">]",
			want: "Per <" + url + ">, done.",
		},
		{
			name: "a workspace link keeps its label",
			draft: "See [auth](internal/auth.go#L4).", raw: "[cite: c1 => internal/auth.go:7-9]",
			want: "See [auth](internal/auth.go#L7-L9).",
		},
		{
			name: "a directive repairs a verification in its ordinary syntax",
			draft: "Done [read#1:1].\n[verify: inspection - Looked]", raw: "[verify: targeted - Ran the package tests]",
			want: "Done [read#1:1].\n\n[verify: targeted - Ran the package tests]",
			check: func(t *testing.T, draft CloseoutDraft) {
				if draft.Verification == nil || draft.Verification.Method != verification.Targeted {
					t.Fatalf("verification = %+v", draft.Verification)
				}
			},
		},
		{
			name: "AC-CLO-10 a full corrected Markdown changing only citations is accepted",
			draft: "One [read#9:1], and [the RFC](https://example.test/old).",
			raw: "One [read#5:1], and [the RFC](" + url + ").",
			want: "One [read#5:1], and [the RFC](" + url + ").",
		},
		{
			name: "AC-CLO-10 a full corrected Markdown may drop an unsupported citation",
			draft: "One [read#9:1], two [read#5:2].", raw: "One, two [read#5:2].",
			want: "One, two [read#5:2].",
		},
		{
			name: "a full correction keeps a directive it left out",
			draft: "One [read#9:1].\n[verify: blocked - No network]", raw: "One [read#5:1].",
			want: "One [read#5:1].\n\n[verify: blocked - No network]",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := repair(retainedFor(tc.draft, false, InvestHandleNotObservedCode), tc.raw, false)
			if len(read.RepairDefects) > 0 {
				t.Fatalf("defects = %+v", read.RepairDefects)
			}
			if read.Draft.Synthesis != tc.want {
				t.Fatalf("source = %q, want %q", read.Draft.Synthesis, tc.want)
			}
			if tc.check != nil {
				tc.check(t, read.Draft)
			}
		})
	}
}

// AC-CLO-09: nothing in a refused repair touches the retained draft.
func TestCitationRepairRefusalsAreAtomic(t *testing.T) {
	const draft = "One [read#9:1], two [read#9:2], and [a link](https://example.test/old)."
	retained := retainedFor(draft, false, InvestHandleNotObservedCode)
	stale := retained
	stale.RepairIDs = map[string]int{"c1": 1, "c2": 1, "c3": 1, "c7": 1}
	for _, tc := range []struct {
		name, raw string
		retained  RetainedCloseout
		reason    RepairDefectReason
		count     int
	}{
		{"unknown target", "[cite: c9 => read#5:1]", retained, RepairUnknownTarget, 1},
		{"stale target", "[cite: c7 => read#5:1]", stale, RepairStaleTarget, 1},
		{"duplicate target", "[cite: c1 => read#5:1]\n[cite: c1 => read#5:2]", retained, RepairDuplicateTarget, 1},
		{"citation for a link", "[cite: c3 => read#5:1]", retained, RepairWrongKind, 1},
		{"link for a citation", "[cite: c1 => <https://example.test/new>]", retained, RepairWrongKind, 1},
		{"one bad target of two applies neither", "[cite: c1 => read#5:1]\n[cite: c9 => read#5:2]", retained, RepairUnknownTarget, 1},
		{"a bag of citations has no mapping", "[read#5:1] [read#5:2]", retained, RepairCitationBag, 1},
		{"a changed conclusion", "One [read#5:1], two [read#5:2], and it was fine.", retained, RepairNarrativeChange, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			read := repair(tc.retained, tc.raw, false)
			if len(read.RepairDefects) != tc.count || read.RepairDefects[0].Reason != tc.reason {
				t.Fatalf("defects = %+v, want %s", read.RepairDefects, tc.reason)
			}
			if read.Draft.Synthesis != draft {
				t.Fatalf("retained draft changed: %q", read.Draft.Synthesis)
			}
		})
	}
}

func TestRepairOutsideACycleIsRefused(t *testing.T) {
	read := ReadCloseoutReport("Done.\n[cite: c1 => read#5:1]", RetainedCloseout{}, false, repairOptions())
	if len(read.RepairDefects) != 1 || read.RepairDefects[0].Reason != RepairOutsideCycle {
		t.Fatalf("defects = %+v", read.RepairDefects)
	}
}

// AC-CLO-18: the key follows the offending value and reason, so moving a repair to
// another ID is the same offender.
func TestCitationRepairKeyIgnoresChangingIDs(t *testing.T) {
	first := GroundingOffenderKey(CitationRepairInvalidCode, CitationRepairRefusalData([]RepairDefect{{Target: "c8", Value: "read#5:1", Reason: RepairUnknownTarget}}))
	second := GroundingOffenderKey(CitationRepairInvalidCode, CitationRepairRefusalData([]RepairDefect{{Target: "c9", Value: "read#5:1", Reason: RepairUnknownTarget}}))
	if first != second {
		t.Fatalf("keys differ: %q %q", first, second)
	}
	other := GroundingOffenderKey(CitationRepairInvalidCode, CitationRepairRefusalData([]RepairDefect{{Target: "c9", Value: "read#6:1", Reason: RepairUnknownTarget}}))
	if other == first {
		t.Fatalf("a new value must be a new offender: %q", other)
	}
}

func TestRetainedRevisionTracksSourceChanges(t *testing.T) {
	retained := retainedFor("One [read#9:1], two [read#9:2].", false)
	if retained.Revision != 1 || retained.RepairIDs["c2"] != 1 {
		t.Fatalf("retained = %+v", retained)
	}
	retained.Retain(retained.Draft)
	if retained.Revision != 1 {
		t.Fatalf("an unchanged draft is the same revision: %d", retained.Revision)
	}
	next := repair(retained, "One [read#5:1].", false)
	if len(next.RepairDefects) != 1 {
		t.Fatalf("changing text must refuse: %+v", next)
	}
	corrected := repair(retained, "One [read#5:1], two.", false).Draft
	retained.Retain(corrected)
	if retained.Revision != 2 || retained.RepairIDs["c2"] != 1 || retained.RepairIDs["c1"] != 2 {
		t.Fatalf("retained = %+v", retained.RepairIDs)
	}
	read := repair(retained, "[cite: c2 => read#5:2]", false)
	if len(read.RepairDefects) != 1 || read.RepairDefects[0].Reason != RepairStaleTarget {
		t.Fatalf("defects = %+v, want an ID from the older revision to be stale", read.RepairDefects)
	}
}

// AC-CLO-14: a fence alone updates the document, rating included, and keeps the
// narrative and its citations.
func TestDocumentOnlyRepair(t *testing.T) {
	const body = "The answer [read#1:2]."
	content := body + "\n\n```json\n{\"headline\":\"Old\",\"rating\":{\"level\":\"low\",\"why\":\"w\"},\"findings\":[{\"id\":\"f1\",\"title\":\"T\",\"disposition\":\"act\"}]}\n```"
	retained := retainedFor(content, true, ReportDocumentInvalidCode)
	read := repair(retained, "```json\n{\"headline\":\"New\",\"rating\":{\"level\":\"high\",\"why\":\"better\"},\"findings\":[{\"id\":\"f1\",\"title\":\"T\",\"disposition\":\"held\"}]}\n```", true)
	if len(read.RepairDefects) > 0 || len(read.Unread) > 0 {
		t.Fatalf("read = %+v", read)
	}
	if read.Draft.Synthesis != body || len(read.Draft.CitedEvidence) != 1 {
		t.Fatalf("narrative or citations changed: %+v", read.Draft)
	}
	doc := read.Draft.Document
	if doc.Headline != "New" || doc.Rating.Level != "high" || doc.Findings[0].Disposition != "held" {
		t.Fatalf("document = %+v", doc)
	}
}

func TestDocumentRepairKeepsThePinnedNarrative(t *testing.T) {
	const body = "The answer [read#1:2]."
	retained := retainedFor(body+"\n\n```json\n{\"headline\":\"Old\"}\n```", true, ReportDocumentInvalidCode)
	read := repair(retained, "A rewritten answer.\n\n```json\n{\"headline\":\"New\"}\n```", true)
	if len(read.RepairDefects) > 0 || read.Draft.Synthesis != body || read.Draft.Document.Headline != "New" {
		t.Fatalf("read = %+v", read)
	}
}

// A citation repair's fence changes finding sources, and no conclusion.
func TestCitationRepairFenceTakesOnlyFindingWhere(t *testing.T) {
	const body = "The answer."
	content := body + "\n\n```json\n{\"headline\":\"Kept\",\"findings\":[{\"id\":\"f1\",\"title\":\"Kept\",\"disposition\":\"act\",\"where\":[{\"evidence\":\"read#9\"}]}]}\n```"
	retained := retainedFor(content, true, SynthHandleNotInLegsCode)
	read := repair(retained, "```json\n{\"headline\":\"Rewritten\",\"findings\":[{\"id\":\"f1\",\"title\":\"Rewritten\",\"disposition\":\"held\",\"where\":[{\"evidence\":\"read#5\"}]}]}\n```", true)
	doc := read.Draft.Document
	if doc.Headline != "Kept" || doc.Findings[0].Title != "Kept" || doc.Findings[0].Disposition != "act" {
		t.Fatalf("a citation repair rewrote conclusions: %+v", doc)
	}
	if doc.Findings[0].Where[0].Evidence != "read#5" {
		t.Fatalf("where = %+v", doc.Findings[0].Where)
	}
	var owners []string
	for _, occurrence := range read.Draft.Occurrences {
		owners = append(owners, occurrence.Owner+"="+occurrence.Citation.Handle)
	}
	if strings.Join(owners, ",") != "findings[0].where[0]=read#5" {
		t.Fatalf("validation sees %v", owners)
	}
}

func TestRepairedDraftRevalidatesDocumentCitations(t *testing.T) {
	content := "One [read#9:1].\n\n```json\n{\"findings\":[{\"id\":\"f1\",\"title\":\"T\",\"where\":[{\"evidence\":\"read#3\"}]}]}\n```"
	retained := retainedFor(content, true, SynthHandleNotInLegsCode)
	read := repair(retained, "[cite: c1 => read#5:1]", true)
	if read.Draft.CitationCount() != 2 {
		t.Fatalf("occurrences = %+v", read.Draft.Occurrences)
	}
}
