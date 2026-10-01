package inject

import (
	"strings"
	"testing"

	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/pkg/api"
)

// The report fence the prompt shows is the report type's own shape: the
// closeout reader takes every member of it, with the declared rating's answers
// inside the finding and the ask once at the top level.
func TestReportDocumentPromptShowsTheShapeTheHostReads(t *testing.T) {
	runCtx := api.CoordinatorRunContext{WorkflowID: "security-survey", RunID: "run-1", CurrentPhase: "report"}
	snap := WorkflowRuntimeSnapshot{
		ReportDocumentEnabled: true,
		Phases:                []WorkflowPhaseRow{{ID: "report"}},
		ReportRating: &ReportRatingView{
			Dimensions: []string{"reachable", "outcome"},
			Questions:  "  - `reachable` — Does untrusted input reach it? `reachable` (Yes), `unknown`\n  - `outcome` — What happens? `none` (None)",
		},
	}
	block := renderTestActiveWorkflow(t, runCtx, snap, nil, nil)
	sectionStart := strings.Index(block, "### Report document")
	if sectionStart < 0 {
		t.Fatalf("workflow prompt has no report section:\n%s", block)
	}
	section := block[sectionStart:]
	open := strings.Index(section, "```json\n")
	if open < 0 {
		t.Fatalf("report section has no opening json fence:\n%s", section)
	}
	end := strings.Index(section[open+len("```json\n"):], "\n```")
	if end < 0 {
		t.Fatalf("report section has no json fence:\n%s", section)
	}
	fence := section[open : open+len("```json\n")+end+len("\n```")]

	read, ok := guidance.ReadCloseoutReport("The answer.\n\n"+fence, "")
	if !ok {
		t.Fatalf("the prompt's fence is not a report:\n%s", fence)
	}
	if len(read.Unread) > 0 {
		t.Fatalf("the prompt's fence has members the host does not read: %+v", read.Unread)
	}
	report := read.Report
	if len(report.Findings) != 1 || report.Findings[0].Answers["reachable"] == "" || report.Findings[0].Answers["outcome"] == "" {
		t.Fatalf("findings = %+v, want the declared answers inside the finding", report.Findings)
	}
	if report.Ask == nil || len(report.SetAsides) != 1 || len(report.CitedEvidence) != 1 {
		t.Fatalf("report = %+v, want ask, set_asides, and cited_evidence at the top level", report)
	}
	if !strings.Contains(section, "\n  - `reachable` — ") {
		t.Fatalf("rating questions are not nested under answers:\n%s", section)
	}
}

// Without a declared rating the fence carries no answers.
func TestReportDocumentPromptWithoutRatingHasNoAnswers(t *testing.T) {
	runCtx := api.CoordinatorRunContext{WorkflowID: "recon", RunID: "run-1", CurrentPhase: "report"}
	block := renderTestActiveWorkflow(t, runCtx, WorkflowRuntimeSnapshot{ReportDocumentEnabled: true, Phases: []WorkflowPhaseRow{{ID: "report"}}}, nil, nil)
	if strings.Contains(block, `"answers"`) {
		t.Fatalf("unrated report shows answers:\n%s", block)
	}
}
