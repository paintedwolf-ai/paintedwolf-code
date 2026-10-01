package worker

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/lycaon/lycaon/internal/session/lifecycle"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerEnqueueRejectedBySessionStopAdmission(t *testing.T) {
	queue := NewInMemoryQueue(1)
	queue.SetSessionAdmission(func(context.Context, string, func() error) error {
		return lifecycle.ErrStopping
	})
	_, err := queue.Enqueue(t.Context(), api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: "parent",
		ProjectID:       testdbseed.DefaultProjectID,
	})
	if !errors.Is(err, lifecycle.ErrStopping) {
		t.Fatalf("enqueue error = %v, want session stopping", err)
	}
	jobs, listErr := queue.ListBySession(t.Context(), testdbseed.DefaultProjectID, "parent", api.WorkerStatusPending)
	testutil.FailErr(t, "list rejected worker admission", listErr)
	if len(jobs) != 0 {
		t.Fatalf("jobs = %+v, want no durable worker after rejected admission", jobs)
	}
}

type cancelOnClaimQueue struct {
	WorkerQueue
	base *InMemoryQueue
}

func (q *cancelOnClaimQueue) ClaimNext(ctx context.Context, req ClaimRequest) (*api.WorkerTask, error) {
	task, err := q.WorkerQueue.ClaimNext(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := q.base.Cancel(ctx, task.ID, nil); err != nil {
		return nil, err
	}
	return task, nil
}

type countingStartExecutor struct {
	calls atomic.Int32
}

func (e *countingStartExecutor) Execute(context.Context, api.WorkerTask, WorkerRunContext) (api.WorkerResult, error) {
	e.calls.Add(1)
	return api.WorkerResult{Status: "complete"}, nil
}

type joinProbeExecutor struct {
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
}

func (e *joinProbeExecutor) Execute(ctx context.Context, _ api.WorkerTask, _ WorkerRunContext) (api.WorkerResult, error) {
	close(e.started)
	<-ctx.Done()
	close(e.canceled)
	<-e.release
	return api.WorkerResult{}, ctx.Err()
}

func TestPollerDoesNotExecuteJobCanceledBetweenClaimAndRegistration(t *testing.T) {
	ctx := context.Background()
	base := NewInMemoryQueue(1)
	queue := &cancelOnClaimQueue{WorkerQueue: base, base: base}
	executor := &countingStartExecutor{}
	poller := NewLocalWorkerPoller(queue, executor, DefaultWorkersConfig(), discardOutcomeRecorder{})
	base.SetRunningCancel(poller.Abort)
	id, err := base.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: "parent",
		ProjectID:       testdbseed.DefaultProjectID,
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue worker", err)

	poller.claimAvailable(ctx)
	poller.inflight.Wait()
	if calls := executor.calls.Load(); calls != 0 {
		t.Fatalf("executor calls = %d, want zero", calls)
	}
	task, ok := base.Get(id)
	if !ok || task.Status != api.WorkerStatusCanceled {
		t.Fatalf("worker = %+v, want canceled", task)
	}
}

func TestPollerAbortWaitsForExecutionExit(t *testing.T) {
	ctx := context.Background()
	queue := NewInMemoryQueue(1)
	executor := &joinProbeExecutor{
		started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
	}
	poller := NewLocalWorkerPoller(queue, executor, DefaultWorkersConfig(), discardOutcomeRecorder{})
	queue.SetRunningCancel(poller.Abort)
	id, err := queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID,
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue worker", err)
	poller.claimAvailable(ctx)
	<-executor.started

	cancelDone := make(chan error, 1)
	go func() { cancelDone <- queue.Cancel(ctx, id, nil) }()
	<-executor.canceled
	select {
	case err := <-cancelDone:
		t.Fatalf("cancel returned before worker execution unwound: %v", err)
	default:
	}
	close(executor.release)
	testutil.FailErr(t, "cancel worker", <-cancelDone)
	poller.cancelMu.Lock()
	registered := poller.cancels[id]
	poller.cancelMu.Unlock()
	if registered != nil {
		t.Fatal("completed execution remained registered")
	}
}

func TestPollerAbortHonorsCallerCancellation(t *testing.T) {
	queue := NewInMemoryQueue(1)
	executor := &joinProbeExecutor{
		started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
	}
	poller := NewLocalWorkerPoller(queue, executor, DefaultWorkersConfig(), discardOutcomeRecorder{})
	id, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID,
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue worker", err)
	poller.claimAvailable(t.Context())
	<-executor.started

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := poller.Abort(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatalf("abort error = %v, want context canceled", err)
	}
	<-executor.canceled
	close(executor.release)
	poller.inflight.Wait()
}

func (*countingStartExecutor) AbortWorkerRuntime(context.Context, api.WorkerTask) error { return nil }

func (*joinProbeExecutor) AbortWorkerRuntime(context.Context, api.WorkerTask) error { return nil }
