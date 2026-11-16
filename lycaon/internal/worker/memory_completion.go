package worker

import (
	"context"

	"github.com/lycaon/lycaon/pkg/api"
)

func (q *memoryQueueClaims) ResumeReadyWaits(context.Context) (int, error) { return 0, nil }

// Complete marks a job completed.
func (q *memoryQueueClaims) Complete(ctx context.Context, claimed *api.WorkerTask, result api.WorkerResult) (bool, error) {
	return admitWorkerTransition(ctx, q.self, claimed, func() (bool, error) {
		return q.completeAdmitted(ctx, claimed, result)
	})
}

// Fail commits a failure only while the claim remains current.
func (q *memoryQueueClaims) Fail(ctx context.Context, claimed *api.WorkerTask, runErr error) (bool, error) {
	return admitWorkerTransition(ctx, q.self, claimed, func() (bool, error) {
		return q.failAdmitted(ctx, claimed, runErr)
	})
}
