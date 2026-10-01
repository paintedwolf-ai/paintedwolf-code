package report

import (
	"strings"
	"testing"
)

// The working summary opens with the closeout's own conclusion, then shows why
// the rating came out as it did and what needs attention.
func TestSummary_OpensWithTheCloseoutsConclusion(t *testing.T) {
	in := briefInput()
	in.Headline = "No exploitable flaw found in the surveyed layers."
	in.Summary = "Read-only survey of three layers."
	joined := joinRowValues(summaryBlocks(testMeasurer(t), in))
	for _, want := range []string{
		sectionSummary, in.Headline, in.Summary,
		"Why this rating", "Reachable", "Worst outcome", "Attacker needs", "Answered by",
		"goldmark XSS", "From the app window", "Already inside", "Review", "Closeout",
		"Needs attention", "Shell-open grant",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("summary = %q, want %q", joined, want)
		}
	}
}

// Held findings are not work to do: they are listed as examined and sound, not
// in the attention table.
func TestSummary_HeldFindingsAreNotAttention(t *testing.T) {
	in := briefInput()
	rows := findingsGlanceRows(testMeasurer(t), in)
	joined := joinRowValues([]block{rowsBlock(rows...)})
	if strings.Contains(joined, "API fails closed") {
		t.Fatalf("attention table lists a held finding: %q", joined)
	}
	sound := joinRowValues(soundBlocks(testMeasurer(t), in))
	if !strings.Contains(sound, "Examined and sound") || !strings.Contains(sound, "API fails closed") {
		t.Fatalf("sound = %q, want the held finding", sound)
	}
}

// Claims are listed by where the review left them, each with the word it gave.
func TestSummary_ClaimsByWhereTheyStand(t *testing.T) {
	in := briefInput()
	in.Claims = []ReportClaim{
		{ID: "api-auth", Title: "The loopback API fails closed", Class: ClaimHeld, Status: "survives"},
		{ID: "sca-reachable", Title: "Dependency exposure", Class: ClaimOpen, Status: "unresolved"},
		{ID: "rollback", Title: "Each step reverses independently", Class: ClaimFailed, Status: "refuted"},
		{ID: "stale", Title: "Stale claim", Class: ClaimOpen, Dropped: true},
	}
	joined := joinRowValues(summaryBlocks(testMeasurer(t), in))
	for _, want := range []string{
		"Open questions", "Unresolved", "Dependency exposure", "sca-reachable",
		"Not revisited", "Stale claim",
		"Overturned by the review", "Refuted", "Each step reverses independently",
		"Confirmed by the review", "Survives", "The loopback API fails closed",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("summary = %q, want %q", joined, want)
		}
	}
}

// What the work did not cover is stated in full: the host's gaps, how the
// scanner inventory was accounted for, the scanner's standing limits, and the
// areas the closeout declared it did not examine.
func TestSummary_NotCoveredInFull(t *testing.T) {
	in := briefInput()
	in.Inventory = &ReportInventory{
		Total: 232, Linked: 16, SetAside: 210, Unaccounted: 6,
		SetAsides: []ReportSetAside{{Reason: "test fixtures", Groups: 210}},
	}
	in.Limits = []string{"MCP consent flow"}
	joined := joinRowValues(summaryBlocks(testMeasurer(t), in))
	for _, want := range []string{
		sectionLimits,
		"232 of 232 scanner result groups have no assessment and no set-aside. Unassessed is not cleared.",
		"1 claim still open: Dependency exposure.",
		"Scanner inventory: 16 of 232 result groups assessed by a claim or finding, 210 set aside, 6 unaccounted.",
		"210 result groups set aside: test fixtures.",
		"Known scanner limits, the same on every run: lycaon-sast could not fully analyze 3431 constructs in 60 files.",
		"Not examined, per the closeout: MCP consent flow.",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("summary = %q, want %q", joined, want)
		}
	}
}

// A report with nothing to summarise renders no section rather than an empty
// heading.
func TestSummary_AbsentWithoutContent(t *testing.T) {
	if got := summaryBlocks(testMeasurer(t), ReportInput{}); got != nil {
		t.Fatalf("summary blocks = %+v, want none", got)
	}
}

func TestLongFindingStatusWrapsInsideItsColumn(t *testing.T) {
	ms := testMeasurer(t)
	cell := gradeCell(ms, 23, ReportFinding{Status: "Unresolved: absent from subsequent review"}, valueProp())
	if _, ok := cell.comp.(*chips); ok {
		t.Fatal("oversized status remained an unbreakable chip")
	}
	if cell.height <= chipHeight(ms) {
		t.Fatal("long status did not gain wrapping height")
	}
}
