package report

import (
	"strings"
	"testing"
)

func assessedFindings() []ReportFinding {
	return []ReportFinding{
		{
			ID: "F1", Title: "Session token compared in variable time", Severity: "high", Status: "open",
			Impact: "An attacker who can time responses recovers the token a byte at a time.",
			Action: "Compare with a constant-time helper.",
			Where: []ReportClaimCitation{
				{Path: "internal/auth/session.go", Line: 88},
				{Path: "internal/auth/token.go", Line: 12},
			},
		},
		{
			ID: "F2", Title: "Egress boundary holds", Status: "verified",
			Impact: "No action needed.",
			Where:  []ReportClaimCitation{{Handle: "read#4"}},
		},
	}
}

// The glance states each finding's grade, its title, and one place, so a
// reader decides what to care about without opening the detail.
func TestFindingsGlance_StatesGradeTitleAndOnePlace(t *testing.T) {
	joined := rowsText(findingsGlanceRows(testMeasurer(t), ReportInput{Findings: assessedFindings()}))
	for _, want := range []string{
		"Severity", "Finding", "Where",
		"High", "Session token compared in variable time", "internal/auth/session.go:88 +1",
		"Verified", "Egress boundary holds", "read#4",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("glance = %q, want substring %q", joined, want)
		}
	}
	// The glance is a decision aid, not the detail: it carries no impact prose.
	if strings.Contains(joined, "byte at a time") {
		t.Fatalf("glance = %q, want the impact left to the detail", joined)
	}
	if len(findingsGlanceRows(testMeasurer(t), ReportInput{})) != 0 {
		t.Fatal("want no glance rows without findings")
	}
}

// A finding with no severity is graded by its status instead, so a surface
// examined and found sound still reads as a stated outcome.
func TestFindingsGlance_StatusGradesWhatSeverityDoesNot(t *testing.T) {
	joined := rowsText(findingsGlanceRows(testMeasurer(t), ReportInput{Findings: []ReportFinding{
		{Title: "Path escape prevented", Status: "verified"},
		{Title: "Nothing graded this"},
	}}))
	if !strings.Contains(joined, "Verified") {
		t.Fatalf("glance = %q, want the status standing in for a grade", joined)
	}
	if !strings.Contains(joined, "Nothing graded this") {
		t.Fatalf("glance = %q, want an ungraded finding still listed", joined)
	}
}

// The detail expands each finding: its number, chips, impact, action, and
// every place it names.
func TestFindingsDetail_ExpandsEachFinding(t *testing.T) {
	joined := joinRowValues(findingsBlocks(testMeasurer(t), ReportInput{Findings: assessedFindings()}))
	for _, want := range []string{
		sectionFindings,
		"1. Session token compared in variable time", "High", "Open", "F1",
		"Impact", "byte at a time", "Action", "constant-time helper",
		"Where", "internal/auth/session.go:88", "internal/auth/token.go:12",
		"2. Egress boundary holds", "read#4",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("detail = %q, want substring %q", joined, want)
		}
	}
}

// A finding keeps its numbering between the glance and the detail, so a reader
// can follow "3" from one to the other.
func TestFindings_NumberingAgreesBetweenGlanceAndDetail(t *testing.T) {
	ms := testMeasurer(t)
	findings := assessedFindings()
	input := ReportInput{Findings: findings}
	glance := rowsText(findingsGlanceRows(ms, input))
	detail := joinRowValues(findingsBlocks(ms, input))
	for i := range findings {
		marker := string(rune('1' + i))
		if !strings.Contains(glance, marker) {
			t.Fatalf("glance = %q, want marker %q", glance, marker)
		}
		if !strings.Contains(detail, marker+". "+findings[i].Title) {
			t.Fatalf("detail = %q, want %q numbered %s", detail, findings[i].Title, marker)
		}
	}
}

func rowsText(rows []measuredRow) string {
	return joinRowValues([]block{rowsBlock(rows...)})
}

// A workflow names its own conclusions. The label reaches the section and its
// column; nothing else about the document changes with it.
func TestFindingsLabel_DeclaredByTheWorkflow(t *testing.T) {
	ms := testMeasurer(t)
	options := ReportInput{
		FindingsLabel: "Options",
		Findings:      []ReportFinding{{Title: "Bounded buffer, reject early", Status: "selected"}},
	}
	glance := rowsText(findingsGlanceRows(ms, options))
	if !strings.Contains(glance, "Option") || strings.Contains(glance, "Finding") {
		t.Fatalf("glance header = %q, want the declared word in its singular", glance)
	}
	detail := joinRowValues(findingsBlocks(ms, options))
	if !strings.Contains(detail, "Options") {
		t.Fatalf("detail = %q, want the declared section name", detail)
	}
	if got := findingsLabel(ReportInput{}); got != sectionFindings {
		t.Fatalf("label = %q, want the renderer's own word when none is declared", got)
	}
}

// The singular is for column headers, and only has to undo plurals the host
// itself writes.
func TestSingular(t *testing.T) {
	for plural, want := range map[string]string{
		"Findings": "Finding", "Options": "Option", "Defects": "Defect",
		"Anomalies": "Anomaly", "Gaps": "Gap", "Risk": "Risk", "Progress": "Progress",
	} {
		if got := singular(plural); got != want {
			t.Fatalf("singular(%q) = %q, want %q", plural, got, want)
		}
	}
}
