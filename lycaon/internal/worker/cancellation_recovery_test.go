package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestCancellationRecoveryContinuesPastCleanupFailure(t *testing.T) {
	for _, backend := range []string{"memory", "sql"} {
		t.Run(backend, func(t *testing.T) {
			q := cancellationQueue(t, backend)
			switch queue := q.(type) {
			case *InMemoryQueue:
				queue.maxRunning = 2
			case *SQLQueue:
				queue.maxRunning = 2
			}
			for range 2 {
				id := enqueueCancellationWorker(t, q)
				claimCancellationWorker(t, q)
				_, err := q.RequestCancellation(t.Context(), id)
				testutil.FailErr(t, "request cancellation", err)
				expireCancellationClaim(t, q, id)
			}
			calls := 0
			cleanupErr := errors.New("first runtime has not exited")
			q.(interface {
				SetRunningCancel(func(context.Context, string) error)
			}).SetRunningCancel(func(context.Context, string) error {
				calls++
				if calls == 1 {
					return cleanupErr
				}
				return nil
			})
			recovered, err := q.RecoverExpiredClaims(t.Context())
			if !errors.Is(err, cleanupErr) || calls != 2 || len(recovered) != 1 || recovered[0].Status != api.WorkerStatusCanceled {
				t.Fatalf("recovery calls=%d outcomes=%+v err=%v", calls, recovered, err)
			}
			recovered, err = q.RecoverExpiredClaims(t.Context())
			testutil.FailErr(t, "retry incomplete cleanup", err)
			if len(recovered) != 1 || recovered[0].Status != api.WorkerStatusCanceled {
				t.Fatalf("remaining cancellation=%+v", recovered)
			}
		})
	}
}

type transientRecoveryQueue struct {
	WorkerQueue
	calls atomic.Int32
}

func (q *transientRecoveryQueue) RecoverExpiredClaims(ctx context.Context) ([]api.WorkerTask, error) {
	if q.calls.Add(1) == 1 {
		return nil, errors.New("transient recovery failure")
	}
	return q.WorkerQueue.RecoverExpiredClaims(ctx)
}

func TestPollerContinuesAfterInitialMaintenanceFailure(t *testing.T) {
	base := NewInMemoryQueue(1)
	id := enqueueCancellationWorker(t, base)
	queue := &transientRecoveryQueue{WorkerQueue: base}
	poller := NewLocalWorkerPoller(queue, &countingStartExecutor{}, DefaultWorkersConfig(), discardOutcomeRecorder{})
	poller.MaintenanceInterval = time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()
	testutil.WaitFor(t, 3*time.Second, func() bool {
		task, ok := base.Get(id)
		return ok && task.Status == api.WorkerStatusComplete && queue.calls.Load() >= 2
	})
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("poller exit=%v", err)
	}
}
