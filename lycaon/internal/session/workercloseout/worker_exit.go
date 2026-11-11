package workercloseout

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/session/workercompletion"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

const workerGroundingHostAssembledProvenance = "grounding_host_assembled"

func hostAssembleWorkerExit(
	ctx context.Context,
	report workercompletion.WorkerCompletionReport,
	opts WorkerSummaryFinalizeOpts,
	childSessionID string,
	blocked workercompletion.WorkerSummaryEvalResult,
) (workercompletion.WorkerCompletionReport, string, workercompletion.WorkerSummaryEvalResult, error) {
	report, assembled, err := bindWorkerObservedSample(ctx, report, opts, childSessionID)
	if err != nil {
		return report, "", workercompletion.WorkerSummaryEvalResult{}, err
	}
	if !assembled {
		return report, "grounding_host_assembly_unavailable", blocked, nil
	}
	blocked.Grounding.HostAssembled = true
	return report, workerGroundingHostAssembledProvenance, blocked, nil
}

func loadChildMessages(ctx context.Context, resolver WorkerSummaryResolver, childSessionID string) ([]api.Message, error) {
	if resolver == nil {
		return nil, fmt.Errorf("session: nil worker summary resolver")
	}
	jobID := workercontext.Job(ctx)
	if jobID == "" {
		return nil, fmt.Errorf("session: worker job id required")
	}
	return resolver.GetWorkerJobMessages(ctx, childSessionID, jobID)
}
