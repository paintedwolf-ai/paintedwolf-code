package workflowadmin

import (
	workflowpresentation "github.com/lycaon/lycaon/internal/workflow/presentation"
	wire "github.com/lycaon/lycaon/pkg/api"
	"testing"
)

// The findings are the closeout's own conclusions, most severe first, with
// their dispositions and answers.
func TestReportFindings_AuthoredOnly(t *testing.T) {
	msg := &wire.Message{CompletionReport: &wire.CompletionReportMeta{
		Findings: []wire.CompletionReportFinding{
			{ID: "F2", Title: "Second", Severity: "medium", Action: "do b", Disposition: wire.CompletionReportFindingDispositionAccept},
			{ID: "F1", Title: "First", Severity: "critical", Impact: "bad", Action: "do a", Disposition: wire.CompletionReportFindingDispositionAct,
				Answers: map[string]string{"reachable": "reachable"},
				Where:   []wire.CompletionReportFindingLocation{{Path: "a.go", Line: 3}}},
			{Title: ""},
		},
	}}
	got := reportFindings(msg, nil, nil)
	if len(got) != 2 || got[0].finding.ID != "F1" || got[1].finding.ID != "F2" {
		t.Fatalf("findings = %+v, want the titled ones, most severe first", got)
	}
	first := got[0]
	if first.finding.Impact != "bad" || first.finding.Where[0].Path != "a.go" || first.finding.Disposition != "act" || first.answers["reachable"] != "reachable" {
		t.Fatalf("finding = %+v, want impact, place, disposition, and answers carried", first)
	}
	if reportFindings(&wire.Message{}, nil, nil) != nil {
		t.Fatal("a report without a completion record states no findings")
	}
}

// Under a declared rating, a finding that needs attention is stated at the
// level its answers decide, with the level's tone, preferring the answers of a
// review claim with its id; the authored word does not survive, and rated
// findings lead the sound ones.
func TestReportFindings_StatesTheRatedLevel(t *testing.T) {
	brief := surveyBrief(t)
	moderate := map[string]string{"reachable": "reachable", "outcome": "degraded", "attacker": "anyone_remote"}
	msg := &wire.Message{CompletionReport: &wire.CompletionReportMeta{
		Findings: []wire.CompletionReportFinding{
			{ID: "sound", Title: "Sound area", Severity: "critical", Disposition: wire.CompletionReportFindingDispositionHeld},
			{ID: "c3", Title: "Config path", Severity: "high", Disposition: wire.CompletionReportFindingDispositionAct},
			{ID: "F2", Title: "Remote slowdown", Severity: "low", Disposition: wire.CompletionReportFindingDispositionAct, Answers: moderate},
		},
	}}
	claims := []workflowpresentation.RunClaim{{ID: "c3", Answers: map[string]string{"reachable": "app_window", "outcome": "limited_misuse", "attacker": "already_inside"}}}
	got := reportFindings(msg, brief, claims)
	if len(got) != 3 || got[0].finding.ID != "F2" || got[1].finding.ID != "c3" || got[2].finding.ID != "sound" {
		t.Fatalf("findings = %+v, want Moderate, then Low, then the sound area", got)
	}
	if got[0].finding.Severity != "Moderate" || got[0].finding.SeverityTone != "medium" {
		t.Fatalf("F2 = %+v, want the Moderate level and its tone", got[0].finding)
	}
	if got[1].finding.Severity != "Low" || got[1].finding.SeverityTone != "low" {
		t.Fatalf("c3 = %+v, want the review's Low level over the authored word", got[1].finding)
	}
	rated := reportBrief(brief, got, claims, nil)
	for i, r := range rated.Rated {
		if want := got[r.Number-1].finding.Severity; rated.Levels[r.Worst].Label != want {
			t.Fatalf("rated[%d] = %s, finding %d states %s; the table and the finding must agree", i, rated.Levels[r.Worst].Label, r.Number, want)
		}
	}
}

// A statement whose sentence break is an abbreviation is not split there.
func TestSplitFirstSentence_KeepsAbbreviations(t *testing.T) {
	title, rest := splitFirstSentence("The value e.g. a token is compared. Then it returns.")
	if title != "The value e.g. a token is compared." || rest != "Then it returns." {
		t.Fatalf("split = %q | %q", title, rest)
	}
	if title, rest := splitFirstSentence("No break here"); title != "No break here" || rest != "" {
		t.Fatalf("split = %q | %q, want the whole statement as the title", title, rest)
	}
	title, rest = splitFirstSentence("Fixed only in the v2 line (2.1.9/2.2.5) while lycaon pins 1.7.33. Low priority.")
	if title != "Fixed only in the v2 line (2.1.9/2.2.5) while lycaon pins 1.7.33." || rest != "Low priority." {
		t.Fatalf("split = %q | %q, want versions kept whole", title, rest)
	}
}
