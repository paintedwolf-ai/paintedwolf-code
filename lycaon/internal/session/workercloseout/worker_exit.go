package workercloseout

import (
	"context"
	"fmt"
	"github.com/lycaon/lycaon/internal/session/workercontext"
	"github.com/lycaon/lycaon/pkg/api"
)

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
