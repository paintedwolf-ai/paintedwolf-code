package app

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/tools"
	workertools "github.com/lycaon/lycaon/internal/tools/native/workercontrol"
)

func (b delegationWiring) decodeCompleteLeg(ctx context.Context, args map[string]any, tctx tools.ToolContext) (workertools.CompleteLegRecord, error) {
	record, err := workercompletion.CompleteLegDecoder(ctx, args, tctx)
	if err != nil || record.LegStatus != "complete" {
		return record, err
	}
	if tctx.Identity.WorkerJobID == "" {
		return record, nil
	}
	task, ok := b.workerQueue.Lookup(ctx, tctx.Identity.WorkerJobID)
	if !ok {
		return record, fmt.Errorf("worker job %q unavailable", tctx.Identity.WorkerJobID)
	}
	report, _ := workercompletion.ReportFromCompleteLegArgs(args)
	return record, b.workflowMgr.Coverage.ValidateCoverageCompletion(ctx, task, report.CoverageReview)
}
