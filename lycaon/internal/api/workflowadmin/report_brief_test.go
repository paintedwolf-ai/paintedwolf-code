package workflowadmin

import (
	"testing"

	"github.com/lycaon/lycaon/internal/report"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
)

// The rating is decided from the findings that need attention: the review's
// answers where a claim shares the finding's id, the closeout's otherwise, and
// every answer unknown where neither can be read.
func TestReportBrief_DecidesFromAttentionFindings(t *testing.T) {
	brief := surveyBrief(t)
	findings := []assembledFinding{
		{finding: report.ReportFinding{ID: "c1", Title: "Reviewed", Disposition: "act"}},
		{finding: report.ReportFinding{Title: "Closeout rated", Disposition: "accept"},
			answers: map[string]string{"reachable": "not_reachable", "outcome": "none", "attacker": "not_applicable"}},
		{finding: report.ReportFinding{Title: "Sound", Disposition: "held"}},
		{finding: report.ReportFinding{Title: "Unreadable", Disposition: "act"}, answers: map[string]string{"reachable": "maybe"}},
	}
	claims := []workflowpresentation.RunClaim{{ID: "c1", Answers: map[string]string{"reachable": "app_window", "outcome": "limited_misuse", "attacker": "already_inside"}}}
	got := reportBrief(brief, findings, claims, nil)
	if len(got.Rated) != 3 || got.Rated[0].Number != 1 || !got.Rated[0].Adjudicated || got.Rated[1].Adjudicated {
		t.Fatalf("rated = %+v, want the three attention findings with the review's answers first", got.Rated)
	}
	if got.Levels[got.Rated[0].Worst].Label != "Low" || got.Levels[got.Rated[1].Worst].Label != "None" {
		t.Fatalf("rated levels = %+v", got.Rated)
	}
	unreadable := got.Rated[2]
	if got.Levels[unreadable.Worst].Label != "Critical" || got.Levels[unreadable.Best].Label != "None" || unreadable.Answers[0] != "Unknown" {
		t.Fatalf("unreadable = %+v, want the whole range", unreadable)
	}
	if got.Levels[got.Worst].Label != "Critical" || got.Levels[got.Best].Label != "Low" {
		t.Fatalf("rating = %s..%s, want Critical..Low", got.Levels[got.Worst].Label, got.Levels[got.Best].Label)
	}
	if reportBrief(nil, findings, claims, nil) != nil {
		t.Fatal("a workflow without a declared rating gets none")
	}
}

// A claim the review left open or overturned that no finding carries is rated
// as its review answered it, or across every answer, never as cleared.
func TestReportBrief_RatesUnreportedClaims(t *testing.T) {
	brief := surveyBrief(t)
	claims := []workflowpresentation.RunClaim{
		{ID: "held", Title: "Held", Class: workflowdef.ClaimHeld},
		{ID: "adv-open", Title: "Advisory still open", Class: workflowdef.ClaimOpen},
		{ID: "refuted", Title: "Fixture triage refuted", Class: workflowdef.ClaimFailed,
			Answers: map[string]string{"reachable": "app_window", "outcome": "limited_misuse", "attacker": "already_inside"}},
	}
	unreported := reportUnreported(nil, claims)
	if len(unreported) != 2 {
		t.Fatalf("unreported = %+v, want the open and the refuted claim", unreported)
	}
	got := reportBrief(brief, nil, claims, unreported)
	if len(got.Rated) != 2 || !got.Rated[0].Unreported || got.Rated[0].Number != 0 || got.Rated[0].Title != "Advisory still open" {
		t.Fatalf("rated = %+v, want the unreported claims rated without a finding number", got.Rated)
	}
	if got.Levels[got.Rated[0].Worst].Label != "Critical" || got.Levels[got.Rated[0].Best].Label != "None" {
		t.Fatalf("an unanswered open claim = %+v, want the whole range", got.Rated[0])
	}
	if !got.Rated[1].Adjudicated || got.Levels[got.Rated[1].Worst].Label != "Low" {
		t.Fatalf("an answered refuted claim = %+v, want its review's rating", got.Rated[1])
	}
	if got.Levels[got.Worst].Label != "Critical" {
		t.Fatalf("rating = %s, want an unstated conclusion to keep the worst case open", got.Levels[got.Worst].Label)
	}
}

// surveyBrief is the shipped security survey's declared rating.
func surveyBrief(t *testing.T) *workflowdef.Brief {
	t.Helper()
	manifests, err := workflowdef.RegistryFromDirs("")
	testutil.FailErr(t, "RegistryFromDirs", err)
	m, err := manifests.Get("security-survey", "1.0.1")
	testutil.FailErr(t, "manifest", err)
	if m.ReportBrief() == nil {
		t.Fatal("security survey declares no rating")
	}
	return m.ReportBrief()
}
