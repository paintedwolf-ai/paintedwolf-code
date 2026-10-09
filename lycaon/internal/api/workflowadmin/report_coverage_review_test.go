package workflowadmin

import (
	"context"
	"errors"
	"github.com/lycaon/lycaon/internal/report"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/testutil"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	"github.com/lycaon/lycaon/internal/workflow/runstate"
	wire "github.com/lycaon/lycaon/pkg/api"
	"testing"
)

type coverageFactsStub struct {
	facts reviewcoverage.Facts
	err   error
}

func (s coverageFactsStub) CoverageFacts(context.Context, *wire.WorkflowRun, workflowdef.Manifest) (reviewcoverage.Facts, error) {
	return s.facts, s.err
}

func coverageManifest() workflowdef.Manifest {
	return workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{{
		ID:         "challenge",
		ReviewLoop: &workflowdef.ReviewLoopDef{VerdictSchema: map[string]string{"verdict": "CHALLENGED", "coverage": workflowdef.VerdictCoverageType}},
	}}}
}

func TestReportCoverageReviewRecordsPendingScansAsAGap(t *testing.T) {
	var input report.ReportInput
	err := appendCoverageReview(context.Background(), &input, coverageFactsStub{err: runstate.ErrCoverageScansPending}, &wire.WorkflowRun{ID: "run"}, coverageManifest(), nil)
	testutil.FailErr(t, "appendCoverageReview with pending scans", err)
	if input.CoverageFacts != nil || input.CoverageReview != nil {
		t.Fatalf("pending scans produced coverage facts: %+v", input)
	}
	if len(input.Gaps) != 1 || input.Gaps[0].Kind != report.GapCoverageUnreviewed || input.Gaps[0].Count != 1 {
		t.Fatalf("gaps = %+v, want one %s gap", input.Gaps, report.GapCoverageUnreviewed)
	}
	if got := input.Completeness(); got != report.CompletenessIncomplete {
		t.Fatalf("completeness = %q, want %q", got, report.CompletenessIncomplete)
	}
}

func TestReportCoverageReviewKeepsHostFaults(t *testing.T) {
	fault := errors.New("ledger unavailable")
	var input report.ReportInput
	err := appendCoverageReview(context.Background(), &input, coverageFactsStub{err: fault}, &wire.WorkflowRun{ID: "run"}, coverageManifest(), nil)
	if !errors.Is(err, fault) {
		t.Fatalf("host fault = %v, want %v", err, fault)
	}
	if len(input.Gaps) != 0 {
		t.Fatalf("host fault recorded as a gap: %+v", input.Gaps)
	}
}

func TestReportCoverageReviewSkipsWorkflowsWithoutCoverage(t *testing.T) {
	var input report.ReportInput
	manifest := workflowdef.Manifest{PhaseDefs: []workflowdef.PhaseDef{{ID: "plan"}}}
	err := appendCoverageReview(context.Background(), &input, coverageFactsStub{err: runstate.ErrCoverageScansPending}, &wire.WorkflowRun{ID: "run"}, manifest, nil)
	testutil.FailErr(t, "appendCoverageReview without coverage", err)
	if len(input.Gaps) != 0 || input.CoverageFacts != nil {
		t.Fatalf("workflow without coverage acquired coverage state: %+v", input)
	}
}
