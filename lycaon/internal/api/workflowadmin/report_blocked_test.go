package workflowadmin

import (
	"strings"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/report"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

func TestBlockedReportUsesFrozenEvidenceWithoutCompletion(t *testing.T) {
	repair := workflow.ReviewRepair{State: "blocked", Phase: "claims", UpdatedAt: time.Now(), Responses: []workflow.ReviewRepairResponse{{ID: "r1"}, {ID: "r2"}, {ID: "r3"}}, Snapshot: &workflow.ReviewSnapshot{
		Vars:      map[string]any{},
		Scans:     []wire.CodeScan{{ID: "scan", ScannerID: "sast", Status: wire.CodeScanStatusComplete, CoverageStatus: wire.ScanCoveragePartial, Warnings: []wire.ScanWarning{{Kind: wire.ScanWarningFilePartialSemantics, File: "src/core.go"}}}},
		Candidate: map[string]any{"verdict": map[string]any{"claims": []any{map[string]any{"id": "candidate", "statement": "Unaccepted candidate observation"}}}},
	}}
	h := Handler{Deps: Deps{Runs: coverageRuns{vars: map[string]any{"review_repairs": []workflow.ReviewRepair{repair}}}}}
	run := &wire.WorkflowRun{ID: "run", CurrentPhase: "claims", Status: wire.WorkflowRunStatusPaused, PauseReason: workflow.ReviewBlockedReason}
	input, ok, err := h.blockedRunReport(t.Context(), run, workflowdef.Manifest{Name: "Security review"})
	testutil.FailErr(t, "build paused snapshot", err)
	if !ok || input.Completeness() != report.CompletenessIncomplete || input.Kind != report.BlockedReviewSnapshot {
		t.Fatalf("snapshot lost incomplete status: %+v", input)
	}
	if len(input.Findings) != 0 || !strings.Contains(input.Synthesis, "Unaccepted candidate") {
		t.Fatal("candidate was promoted or omitted")
	}
	if input.Inventory == nil || len(input.Gaps) == 0 {
		t.Fatal("frozen scan limitations missing")
	}
	run.Status = wire.WorkflowRunStatusCanceled
	input, ok, err = h.blockedRunReport(t.Context(), run, workflowdef.Manifest{Name: "Security review"})
	testutil.FailErr(t, "build canceled snapshot", err)
	if !ok || input.Completeness() != report.CompletenessIncomplete {
		t.Fatal("cancel lost snapshot")
	}
}

// Worker observations and gaps reach the snapshot labelled as unadjudicated,
// and each required review phase reports that no terminal verdict exists.
func TestBlockedReportLabelsWorkerWorkAndOpenReviews(t *testing.T) {
	worker := wire.WorkerTask{ID: "t1", AgentType: "security-reviewer", Status: wire.WorkerStatusComplete, Result: &wire.WorkerResult{
		CompletionReport: &wire.WorkerCompletionReport{
			LegStatus: "partial", Brief: "Reviewed the auth layer.", ObjectivesMet: []string{"token handling"}, RemainingRisk: []string{"session fixation"},
			Findings:     []wire.WorkerCompletionFinding{{Path: "auth.go", Line: 12, Claim: "token reuse", Evidence: "ev-1"}},
			CoverageGaps: []wire.WorkerCoverageGap{{ID: "gap-1", Subject: "admin routes", Reason: "timed out", Paths: []string{"admin/"}}},
		},
	}}
	repair := workflow.ReviewRepair{State: "blocked", Phase: "claims", UpdatedAt: time.Now(), Diagnostics: []wire.ToolFeedback{{Code: "TOOL_ARGS_INVALID", Details: map[string]any{"reason": "coverage is not an object"}}},
		Snapshot: &workflow.ReviewSnapshot{Vars: map[string]any{}, Workers: []wire.WorkerTask{worker}, Unavailable: []string{"scan ledger"}}}
	manifest := workflowdef.Manifest{Name: "Security review", PhaseDefs: []workflowdef.PhaseDef{{ID: "claims", ActivityLabel: "Stating claims", ReviewLoop: &workflowdef.ReviewLoopDef{RequiredAgents: []string{"skeptic"}}}}}
	h := Handler{Deps: Deps{Runs: coverageRuns{vars: map[string]any{"review_repairs": []workflow.ReviewRepair{repair}}}}}
	run := &wire.WorkflowRun{ID: "run", CurrentPhase: "claims", Status: wire.WorkflowRunStatusPaused, PauseReason: workflow.ReviewBlockedReason}
	input, ok, err := h.blockedRunReport(t.Context(), run, manifest)
	testutil.FailErr(t, "build snapshot", err)
	if !ok || len(input.Findings) != 0 {
		t.Fatalf("worker observations became findings: ok %v %+v", ok, input.Findings)
	}
	for _, want := range []string{
		"TOOL_ARGS_INVALID", "coverage is not an object", "Unavailable when paused: scan ledger.",
		"Worker observation, not adjudicated: token reuse", "Unresolved worker scope gap-1: admin routes — timed out", "Affected paths: admin/",
	} {
		if !strings.Contains(input.Synthesis, want) {
			t.Fatalf("snapshot synthesis lacks %q:\n%s", want, input.Synthesis)
		}
	}
	var review *report.ReportCoverageItem
	for i := range input.Coverage {
		if input.Coverage[i].Subject == "Stating claims" {
			review = &input.Coverage[i]
		}
	}
	if review == nil || review.Status != "No terminal verdict recorded" || !strings.Contains(review.Detail, "skeptic") {
		t.Fatalf("review coverage = %+v", input.Coverage)
	}
}

// Only a blocked episode for the current phase yields a snapshot.
func TestBlockedReportNeedsABlockedEpisodeForTheCurrentPhase(t *testing.T) {
	run := &wire.WorkflowRun{ID: "run", CurrentPhase: "claims", Status: wire.WorkflowRunStatusPaused, PauseReason: workflow.ReviewBlockedReason}
	for name, repair := range map[string]workflow.ReviewRepair{
		"repairing":   {State: "repairing", Phase: "claims", Snapshot: &workflow.ReviewSnapshot{}},
		"other phase": {State: "blocked", Phase: "challenge", Snapshot: &workflow.ReviewSnapshot{}},
	} {
		h := Handler{Deps: Deps{Runs: coverageRuns{vars: map[string]any{"review_repairs": []workflow.ReviewRepair{repair}}}}}
		if _, ok, err := h.blockedRunReport(t.Context(), run, workflowdef.Manifest{}); ok || err != nil {
			t.Fatalf("%s: ok %v err %v", name, ok, err)
		}
	}
}
