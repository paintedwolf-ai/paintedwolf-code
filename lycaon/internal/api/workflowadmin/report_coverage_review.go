package workflowadmin

import (
	"context"
	"errors"

	"github.com/lycaon/lycaon/internal/report"
	"github.com/lycaon/lycaon/internal/reviewcoverage"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// coverageFactsSource is the run manager's view of settled coverage facts.
type coverageFactsSource interface {
	CoverageFacts(ctx context.Context, run *wire.WorkflowRun, manifest workflowdef.Manifest) (reviewcoverage.Facts, error)
}

// appendCoverageReview projects acceptance from the final declared review
// phase. A run that ended before its scans settled has no facts to check an
// assessment against; the report records that as a gap rather than failing.
func appendCoverageReview(ctx context.Context, input *report.ReportInput, runs coverageFactsSource, run *wire.WorkflowRun, manifest workflowdef.Manifest, phaseVerdicts []workflow.PhaseVerdict) error {
	last := ""
	for _, phase := range manifest.PhaseDefs {
		if phase.ReviewLoop != nil && phase.ReviewLoop.CarriesCoverage() {
			last = phase.ID
		}
	}
	if last == "" {
		return nil
	}
	facts, err := runs.CoverageFacts(ctx, run, manifest)
	if errors.Is(err, workflow.ErrCoverageScansPending) {
		input.Gaps = append(input.Gaps, report.ReportGap{Kind: report.GapCoverageUnreviewed, Count: 1})
		return nil
	}
	if err != nil {
		return err
	}
	input.CoverageFacts, input.CoverageReview = &facts, nil
	for _, verdict := range phaseVerdicts {
		if verdict.Phase == last && verdict.Record.GateVerdict == "approved" {
			input.CoverageReview = workflow.RunCoverageReview(phaseVerdicts)
			break
		}
	}
	return nil
}
