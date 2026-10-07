package report

import (
	"strings"
	"testing"
)

// securityBrief is the security survey's declared scale, decided Low.
func securityBrief() *ReportBrief {
	return &ReportBrief{
		Question: "How serious is it?",
		Levels: []ReportLevel{
			{Label: "Critical", Answer: "Critical risk", Means: "Act now.", Tone: "critical"},
			{Label: "High", Answer: "High risk", Means: "Fix before the next release.", Tone: "high"},
			{Label: "Moderate", Answer: "Moderate risk", Means: "A real flaw exists.", Tone: "medium"},
			{Label: "Low", Answer: "Low risk", Means: "Minor issues only. Nothing urgent.", Tone: "low"},
			{Label: "None", Answer: "No known risk", Means: "Nothing found that needs action.", Tone: "good"},
		},
		Worst: 3, Best: 3,
		Basis:      "only someone already in control of the app could use it",
		Dimensions: []string{"Reachable", "Worst outcome", "Attacker needs"},
		Rated: []ReportRated{
			{Number: 1, Title: "goldmark XSS", Answers: []string{"No", "None", "Not applicable"}, Worst: 4, Best: 4, Adjudicated: true},
			{Number: 2, Title: "Shell-open grant", Answers: []string{"From the app window", "Limited misuse", "Already inside"}, Worst: 3, Best: 3},
		},
	}
}

// briefInput is the survey run as its records left it.
func briefInput() ReportInput {
	return ReportInput{
		Title: "Security survey", Project: "lycaon", RunID: "9d6fda3b-8b63-5dbe-baaa-11f13b985143",
		CompletedAt: "2026-09-18T22:50:08Z", Synthesis: "ok",
		Brief: securityBrief(),
		Ask:   &ReportAsk{Do: "Approve a routine update to one outside software component.", Effort: "small", Why: "The fix is available."},
		Findings: []ReportFinding{
			{Title: "goldmark XSS", Severity: "medium", Disposition: DispositionAct, Where: []ReportClaimCitation{{Path: "lycaon/go.mod", Line: 58}}},
			{Title: "Shell-open grant", Severity: "low", Disposition: DispositionAct},
			{Title: "API fails closed", Disposition: DispositionHeld},
		},
		Gaps: []ReportGap{
			{Kind: GapInventoryUnaccounted, Count: 232, Of: 232},
			{Kind: GapClaimsOpen, Count: 1, Of: 13, Names: []string{"Dependency exposure"}},
			{Kind: GapScansStanding, Count: 1, Of: 3, Names: []string{"lycaon-sast"}, Detail: 3431, DetailFiles: 60},
		},
		Checks: []ReportCheck{
			{Kind: CheckArea, Subject: "Background service", State: CheckDone},
			{Kind: CheckArea, Subject: "Desktop app", State: CheckPartial},
			{Kind: CheckReview, Subject: "Second opinion", State: CheckPartial, Held: 12, Open: 1},
			{Kind: CheckScans, State: CheckUnchecked, Ran: 3, Used: 0, Total: 232},
		},
	}
}

// The page opens with the rating and how complete the work is, both in the
// host's words.
func TestBrief_AnswerLineStatesRatingThenCompleteness(t *testing.T) {
	joined := joinRowValues(briefBlocks(testMeasurer(t), briefInput()))
	for _, want := range []string{
		"Low risk.", "The check is incomplete.",
		"How serious is it?", "Minor issues only. Nothing urgent.",
		"Worst of 2 findings: only someone already in control of the app could use it.",
		"How complete is the check?",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("brief = %q, want %q", joined, want)
		}
	}
}

// An unknown answer that could decide a more severe level leaves a range,
// stated as the certain level and the one it could reach.
func TestBrief_RangeWhenAnOpenAnswerCouldDecide(t *testing.T) {
	in := briefInput()
	in.Brief.Worst = 2
	joined := joinRowValues(briefBlocks(testMeasurer(t), in))
	for _, want := range []string{"Low risk, possibly moderate.", "Low to moderate", "An open answer could make it moderate."} {
		if !strings.Contains(joined, want) {
			t.Fatalf("brief = %q, want %q", joined, want)
		}
	}
}

// Open answers prevent a conclusive rating.
func TestBrief_OpenRatingIsNotRated(t *testing.T) {
	in := briefInput()
	in.Brief.Best, in.Brief.Worst = 4, 0
	joined := joinRowValues(briefBlocks(testMeasurer(t), in))
	for _, want := range []string{"Not rated, possibly critical.", "Open answers leave it anywhere from none to critical."} {
		if !strings.Contains(joined, want) {
			t.Fatalf("brief = %q, want %q", joined, want)
		}
	}
	if strings.Contains(joined, "No known risk") {
		t.Fatalf("an open rating stated as no known risk: %q", joined)
	}
}

// A report stored with members the host could not read says so plainly.
func TestBrief_UnreadFenceIsNamed(t *testing.T) {
	in := briefInput()
	in.Defects = []ReportDefect{{Code: DefectFenceUnreadable, Reason: "these members are not report fields", Subjects: []string{"`findings[].ask` (13): `ask` is a top-level report field"}, Count: 1}}
	joined := joinRowValues(briefBlocks(testMeasurer(t), in))
	if !strings.Contains(joined, "Some of what it wrote was not in the report's format and was left out.") || strings.Contains(joined, "findings[]") {
		t.Fatalf("brief = %q", joined)
	}
}

// Completeness names the review's unfinished work; it never asks the reader
// to do it.
func TestBrief_CompletenessReasonNamesGapsNotTasks(t *testing.T) {
	joined := joinRowValues(briefBlocks(testMeasurer(t), briefInput()))
	want := "The review didn't use the automated scan results, and 1 question is still open."
	if !strings.Contains(joined, want) {
		t.Fatalf("brief = %q, want %q", joined, want)
	}
	for _, banned := range []string{"Have someone review", "Before calling this done", "Resolve"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("brief assigns work to the reader: %q in %q", banned, joined)
		}
	}
}

// Scanner limits that recur on every run describe the scanner and leave the
// work complete; moved files leave it mostly complete; open claims and
// unaccounted groups leave it incomplete.
func TestCompleteness_ByKindOfGap(t *testing.T) {
	cases := []struct {
		gaps []ReportGap
		want string
	}{
		{nil, CompletenessComplete},
		{[]ReportGap{{Kind: GapScansStanding, Count: 2}}, CompletenessComplete},
		{[]ReportGap{{Kind: GapScansMoved, Count: 1}}, CompletenessMostly},
		{[]ReportGap{{Kind: GapLegsPartial, Count: 1}, {Kind: GapWorkersPartial, Count: 2}}, CompletenessMostly},
		{[]ReportGap{{Kind: GapScansMoved, Count: 1}, {Kind: GapClaimsOpen, Count: 1}}, CompletenessIncomplete},
		{[]ReportGap{{Kind: GapInventoryUnaccounted, Count: 3}}, CompletenessIncomplete},
		{[]ReportGap{{Kind: GapCoverageUnreviewed, Count: 1}}, CompletenessIncomplete},
		{[]ReportGap{{Kind: GapClaimsOpen, Count: 0}}, CompletenessComplete},
	}
	for i, tc := range cases {
		if got := (ReportInput{Gaps: tc.gaps}).Completeness(); got != tc.want {
			t.Fatalf("case %d: completeness = %q, want %q", i, got, tc.want)
		}
	}
}

// The ask is the one thing addressed to the reader; without one the page says
// nothing is needed.
func TestBrief_AskOrNothing(t *testing.T) {
	in := briefInput()
	joined := joinRowValues(briefBlocks(testMeasurer(t), in))
	for _, want := range []string{"What we need from you", in.Ask.Do, "Effort: Small", "The fix is available."} {
		if !strings.Contains(joined, want) {
			t.Fatalf("brief = %q, want %q", joined, want)
		}
	}
	in.Ask = nil
	joined = joinRowValues(briefBlocks(testMeasurer(t), in))
	if !strings.Contains(joined, "Nothing. This is for your information.") {
		t.Fatalf("brief without an ask = %q", joined)
	}
	in.UnreportedClaims = 3
	joined = joinRowValues(briefBlocks(testMeasurer(t), in))
	if !strings.Contains(joined, "No decision was recorded. 3 review results still need a conclusion.") || strings.Contains(joined, "Nothing.") {
		t.Fatalf("brief with unstated review results = %q", joined)
	}
}

// A report stored without being accepted says so before its rating, in plain
// words on the first page and with what each check named on the second.
func TestBrief_NotAcceptedLeadsThePage(t *testing.T) {
	in := briefInput()
	in.Defects = []ReportDefect{
		{Code: DefectClaimUnreported, Reason: "claims the review left open or overturned need a finding with the same id", Subjects: []string{"c1 (failed)", "c2 (open)"}, Count: 3},
		{Code: DefectInventoryUnaccounted, Reason: "215 of 234 scanner groups have no assessment and no set-aside", Subjects: []string{"group:a · secrets · high · rule"}, Count: 215},
		{Code: DefectDocumentInvalid, Reason: `finding "x" has no disposition (want act, accept, or held)`},
	}
	blocks := briefBlocks(testMeasurer(t), in)
	joined := joinRowValues(blocks)
	for _, want := range []string{
		"Report not accepted",
		"This report failed its own checks and is stored as written, so it is incomplete.",
		"3 review results have no conclusion in it.",
		"215 automated scan result groups have no assessment and no set-aside.",
		"Some of its fields break the report format.",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("brief = %q, want %q", joined, want)
		}
	}
	if strings.Index(joined, "Report not accepted") > strings.Index(joined, "Low risk.") {
		t.Fatalf("brief = %q, want the notice ahead of the rating", joined)
	}
	for _, banned := range []string{"c1", "group:"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("brief carries %q: %q", banned, joined)
		}
	}
	if h := blocks[0].height(); h > usableHeight {
		t.Fatalf("brief is %.1fmm tall; one page holds %.1fmm", h, usableHeight)
	}

	summary := joinRowValues(summaryBlocks(testMeasurer(t), in))
	for _, want := range []string{
		"Why the report was not accepted",
		"Claims the review left open or overturned need a finding with the same id. c1 (failed); c2 (open); and 1 more.",
		`Finding "x" has no disposition (want act, accept, or held).`,
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary = %q, want %q", summary, want)
		}
	}
}

// Each check is worded by its kind: areas by their state, a review by its
// conclusions, scans by whether the review used their results.
func TestBrief_ChecksWordedByKind(t *testing.T) {
	joined := joinRowValues(briefBlocks(testMeasurer(t), briefInput()))
	for _, want := range []string{
		"What was checked",
		"Background service", "Checked",
		"Desktop app", "Partly checked",
		"Second opinion on 13 conclusions", "12 confirmed, 1 open",
		"Automated scans (3 ran)", "Results not used by the review",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("brief = %q, want %q", joined, want)
		}
	}
}

// A workflow that declares no rating gets the completeness gauge alone, and
// the opening line states completeness alone.
func TestBrief_WithoutARatingStatesCompleteness(t *testing.T) {
	in := briefInput()
	in.Brief = nil
	in.Gaps = nil
	joined := joinRowValues(briefBlocks(testMeasurer(t), in))
	if strings.Contains(joined, "How serious is it?") || strings.Contains(joined, "risk") {
		t.Fatalf("brief without a rating = %q, want no rating", joined)
	}
	for _, want := range []string{"The check is complete.", "The planned review is complete. Its scope and limitations are documented below."} {
		if !strings.Contains(joined, want) {
			t.Fatalf("brief = %q, want %q", joined, want)
		}
	}
}

// The brief is one page: it ends the page, and no identifier, path, or
// severity chip appears on it.
func TestBrief_IsOnePageWithoutIdentifiers(t *testing.T) {
	in := briefInput()
	blocks := briefBlocks(testMeasurer(t), in)
	if len(blocks) != 1 || !blocks[0].breakAfter {
		t.Fatalf("brief blocks = %d, want one block that ends the page", len(blocks))
	}
	if h := blocks[0].height(); h > usableHeight {
		t.Fatalf("brief is %.1fmm tall; one page holds %.1fmm", h, usableHeight)
	}
	joined := joinRowValues(blocks)
	for _, banned := range []string{"9d6fda3b", "lycaon/go.mod", "Medium", "group:"} {
		if strings.Contains(joined, banned) {
			t.Fatalf("brief carries %q: %q", banned, joined)
		}
	}
	if !strings.Contains(joined, "2 findings need attention") {
		t.Fatalf("brief = %q, want the attention count pointing to the next page", joined)
	}
}

// A report with acceptance defects is incomplete even when gaps are empty.
func TestBrief_NotAcceptedReportIsIncomplete(t *testing.T) {
	in := briefInput()
	in.Defects = []ReportDefect{{Code: "inventory_unaccounted", Reason: "unaccounted"}}
	if got := in.Completeness(); got != CompletenessIncomplete {
		t.Fatalf("in.Completeness() = %q, want %q", got, CompletenessIncomplete)
	}
	joined := joinRowValues(briefBlocks(testMeasurer(t), in))
	for _, want := range []string{
		"The check is incomplete.",
		"This report failed acceptance checks and is not complete.",
		"Report not accepted",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("brief = %q, want %q", joined, want)
		}
	}
}
