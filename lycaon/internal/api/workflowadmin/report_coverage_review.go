package workflowadmin

import (
	"context"

	"github.com/lycaon/lycaon/internal/report"
	"github.com/lycaon/lycaon/internal/workflow"
	workflowdef "github.com/lycaon/lycaon/internal/workflow/definition"
	wire "github.com/lycaon/lycaon/pkg/api"
)

// appendCoverageReview projects acceptance from the final declared review phase.
func appendCoverageReview(ctx context.Context, input *report.ReportInput, runs *workflow.RunManager, run *wire.WorkflowRun, manifest workflowdef.Manifest, phaseVerdicts []workflow.PhaseVerdict) error {
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
