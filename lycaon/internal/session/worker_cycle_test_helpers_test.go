package session

import (
	"context"
	"fmt"

	"github.com/lycaon/lycaon/pkg/api"
)

type noopWorkerBranchClaim struct{}

func (noopWorkerBranchClaim) ClaimWorkerBranch(context.Context, string) (*api.WorkerTask, error) {
	return nil, fmt.Errorf("worker branch claim not available")
}
