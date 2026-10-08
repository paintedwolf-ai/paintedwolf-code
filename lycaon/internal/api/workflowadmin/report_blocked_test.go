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
	h := Handler{Runs: coverageRuns{vars: map[string]any{"review_repairs": []workflow.ReviewRepair{repair}}}}
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
