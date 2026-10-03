package report

import (
	"strings"
	"testing"
)

func TestUnresolvedFindingIsAnUnratedOpenQuestion(t *testing.T) {
	finding := ReportFinding{ID: "consent", Title: "Initial consent remains untraced", Disposition: DispositionUnresolved}
	if finding.NeedsAttention() {
		t.Fatal("unanswered question acquired a risk rating")
	}
	input := ReportInput{Findings: []ReportFinding{finding}, Claims: []ReportClaim{{ID: finding.ID, Title: finding.Title, Class: ClaimOpen, Status: "unresolved"}}}
	ms := testMeasurer(t)
	if got := soundBlocks(ms, input); len(got) != 0 {
		t.Fatal("unanswered question presented as examined and sound")
	}
	summary := joinRowValues(summaryBlocks(ms, input))
	if !strings.Contains(summary, "Open questions") || !strings.Contains(summary, finding.Title) {
		t.Fatalf("question missing from summary: %s", summary)
	}
	if word := dispositionWord(DispositionUnresolved); word != "Unanswered question" {
		t.Fatalf("question disposition = %q", word)
	}
}
