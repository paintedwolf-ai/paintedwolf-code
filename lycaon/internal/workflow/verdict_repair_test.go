package workflow

import (
	"github.com/lycaon/lycaon/internal/guidance"
	"github.com/lycaon/lycaon/internal/toolrejection"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"strings"
	"testing"
)

func TestVerdictRepairReportsIndependentShapeErrors(t *testing.T) {
	def := workflowdef.ReviewLoopDef{VerdictSchema: map[string]string{"verdict": "ACCEPTED|REVISE", "coverage": workflowdef.VerdictCoverageType, "claims": workflowdef.VerdictClaimsType, "explanation": "string"}, ClaimStatuses: map[string]workflowdef.ClaimClass{"claimed": workflowdef.ClaimOpen}}
	err := ValidateReviewLoopVerdict(def, map[string]string{"verdict": "WRONG", "unexpected": "value", "claims": `[{"id":"c1","statement":"Claim","status":"bad","title":"Claim"}]`, "coverage": `{"revision":"x","assessments":[]}`}, VerdictRules{})
	if err == nil {
		t.Fatal("invalid shape accepted")
	}
	for _, detail := range []string{"WRONG", "unexpected", "explanation", "bad"} {
		if !strings.Contains(err.Error(), detail) {
			t.Fatalf("repair omits %s: %v", detail, err)
		}
	}
}

func TestVerdictRepairsRetainEveryStructuredCode(t *testing.T) {
	out := ReviewLoopVerdictOutcome{
		InventoryIssue: &InventoryIssue{ReportDocumentIssue: guidance.ReportDocumentIssue{Code: SubmitVerdictScansPendingCode}},
		MissingAgents:  []string{"skeptic"},
		GroundingCode:  "SUBMIT_VERDICT_UNGROUNDED",
		QuestionIssue:  toolrejection.AsToolReject(rejectReviewQuestion("current_review_required", "question/c1")),
		CoverageIssue:  &toolrejection.ToolReject{Code: ReviewLoopVerdictInvalidCode, Data: map[string]any{"reason": "stale coverage revision"}},
	}
	repairs := verdictRepairs("", out)
	want := []string{SubmitVerdictScansPendingCode, SubmitVerdictReviewerMissingCode, out.GroundingCode, submitVerdictQuestionInvalidCode, ReviewLoopVerdictInvalidCode}
	if len(repairs) != len(want) {
		t.Fatalf("repairs = %+v", repairs)
	}
	for i, code := range want {
		if repairs[i].Code != code {
			t.Fatalf("repair %d = %s, want %s", i, repairs[i].Code, code)
		}
	}
	if repairs[3].Details["question_id"] != "question/c1" {
		t.Fatal("question subject lost in aggregate repair")
	}
	if repairs[4].Details["reason"] != "stale coverage revision" {
		t.Fatal("coverage refusal lost its structured reason")
	}
}

func TestVerdictRepairsStateEachCodeOnce(t *testing.T) {
	out := ReviewLoopVerdictOutcome{
		InventoryIssue: &InventoryIssue{ReportDocumentIssue: guidance.ReportDocumentIssue{Code: SubmitVerdictScansPendingCode}},
		CoverageIssue:  &toolrejection.ToolReject{Code: SubmitVerdictScansPendingCode, Data: map[string]any{}},
	}
	repairs := verdictRepairs("", out)
	if len(repairs) != 1 || repairs[0].Code != SubmitVerdictScansPendingCode {
		t.Fatalf("pending scans repeated across channels: %+v", repairs)
	}
}

func TestVerdictInventoryRepairUsesToolCode(t *testing.T) {
	repairs := verdictRepairs("", ReviewLoopVerdictOutcome{InventoryIssue: &InventoryIssue{ReportDocumentIssue: guidance.ReportDocumentIssue{Code: guidance.ReportInventoryUnaccountedCode}}})
	if len(repairs) != 1 || repairs[0].Code != SubmitVerdictInventoryUnaccountedCode {
		t.Fatalf("report code escaped into verdict rejection: %+v", repairs)
	}
}
