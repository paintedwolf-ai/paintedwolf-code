package report_test

import (
	"testing"

	"github.com/lycaon/lycaon/internal/report/reporttest"
)

func TestReportInputFixture_SecuritySurvey(t *testing.T) {
	input := reporttest.LoadReportInputFixture(t, "security_survey.json")
	reporttest.AssertReportInput(t, input)

	if input.Headline == "" || input.Summary == "" || len(input.Limits) == 0 {
		t.Fatalf("security_survey: want the summary layer; headline=%q summary=%q limits=%d",
			input.Headline, input.Summary, len(input.Limits))
	}
	// The summary layer is what a reader who stops there takes away, so the
	// fixture must exercise a graded finding, an ungraded one, and an action.
	var sawGraded, sawUngraded, sawAction bool
	for _, f := range input.Findings {
		if f.Severity != "" {
			sawGraded = true
		} else {
			sawUngraded = true
		}
		if f.Action != "" {
			sawAction = true
		}
	}
	if !sawGraded || !sawUngraded || !sawAction {
		t.Fatalf("security_survey: want graded and ungraded findings with actions; graded=%v ungraded=%v action=%v",
			sawGraded, sawUngraded, sawAction)
	}
	// Two review phases: the one that named the claims and the one that
	// challenged them. A report that kept only the last would lose the claims.
	if len(input.Verdicts) < 2 {
		t.Fatalf("security_survey: want a verdict timeline; verdicts=%d", len(input.Verdicts))
	}
	if len(input.Verdicts[0].Fields) == 0 || len(input.Verdicts[0].Claims) == 0 {
		t.Fatalf("security_survey: want schema fields and claims on the first verdict; fields=%d claims=%d",
			len(input.Verdicts[0].Fields), len(input.Verdicts[0].Claims))
	}
	var sawStatus bool
	for _, c := range input.Verdicts[len(input.Verdicts)-1].Claims {
		if c.Status != "" {
			sawStatus = true
		}
	}
	if !sawStatus {
		t.Fatal("security_survey: want the terminal verdict's claims to carry a status")
	}
	if len(input.ScanRows) == 0 || len(input.ScanRules) == 0 {
		t.Fatalf("security_survey: want scan rows and their rules; rows=%d rules=%d", len(input.ScanRows), len(input.ScanRules))
	}
	// The fixture must carry a scan whose arithmetic needs every term, or
	// nothing exercises the accounting of reported rows.
	if s := input.Scan; s == nil || s.Stored <= s.Listed || s.Ignored == 0 || s.Merged == 0 {
		t.Fatalf("security_survey: want a scan with stored, listed, ignored and merged rows; scan=%+v", input.Scan)
	}
	if input.Scan.WithheldAtIngest() == 0 || input.Scan.NotListed() == 0 {
		t.Fatalf("security_survey: want both withholdings exercised; scan=%+v", input.Scan)
	}
	var sawCited, sawUncited, sawSearch, sawSpan, sawPage bool
	for _, e := range input.Evidence {
		if e.Cited() {
			sawCited = true
		} else {
			sawUncited = true
		}
		if e.Matches > 0 {
			sawSearch = true
		}
		if e.LineEnd > e.Line && e.Line > 0 {
			sawSpan = true
		}
		if e.URL != "" {
			sawPage = true
		}
	}
	if !sawCited || !sawUncited {
		t.Fatalf("security_survey: want cited and uncited evidence; cited=%v uncited=%v", sawCited, sawUncited)
	}
	if !sawSearch || !sawSpan || !sawPage {
		t.Fatalf("security_survey: want search, span and page facts on evidence; search=%v span=%v page=%v", sawSearch, sawSpan, sawPage)
	}
	if input.EvidenceTotal <= len(input.Evidence) {
		t.Fatalf("security_survey: want a ledger larger than the listed sample; total=%d listed=%d", input.EvidenceTotal, len(input.Evidence))
	}
	if len(input.Sources) == 0 {
		t.Fatal("security_survey: want sources")
	}
	var sawTitled bool
	for _, s := range input.Sources {
		if s.Title != "" {
			sawTitled = true
		}
	}
	if !sawTitled {
		t.Fatal("security_survey: want at least one titled source")
	}
	if input.Workforce == nil || input.Workflow == nil || input.StartedAt == "" {
		t.Fatalf("security_survey: want workforce, workflow and started_at for the colophon; got %+v %+v %q", input.Workforce, input.Workflow, input.StartedAt)
	}
	if len(input.Artifacts) < 2 {
		t.Fatalf("security_survey: artifacts len = %d want >= 2 (capture + render)", len(input.Artifacts))
	}
	var sawCapture, sawRender bool
	for _, a := range input.Artifacts {
		if a.EvidenceHandle != "" {
			sawCapture = true
		} else {
			sawRender = true
		}
		if len(a.Bytes) == 0 {
			t.Fatalf("artifact %s: fixture bytes missing", a.ID)
		}
	}
	if !sawCapture || !sawRender {
		t.Fatalf("security_survey: want capture+render artifacts; capture=%v render=%v", sawCapture, sawRender)
	}
}

func TestReportInputFixture_SynthesisOnly(t *testing.T) {
	input := reporttest.LoadReportInputFixture(t, "synthesis_only.json")
	reporttest.AssertReportInput(t, input)

	if len(input.Verdicts) != 0 {
		t.Fatal("synthesis_only: want no verdicts (no empty section stub)")
	}
	if len(input.Findings) != 0 || len(input.ScanRows) != 0 {
		t.Fatalf("synthesis_only: findings=%d scan rows=%d want none", len(input.Findings), len(input.ScanRows))
	}
	if len(input.Evidence) != 0 {
		t.Fatalf("synthesis_only: evidence len = %d want 0", len(input.Evidence))
	}
	if len(input.Sources) != 0 {
		t.Fatalf("synthesis_only: sources len = %d want 0", len(input.Sources))
	}
	if len(input.Artifacts) != 0 {
		t.Fatalf("synthesis_only: artifacts len = %d want 0", len(input.Artifacts))
	}
}
