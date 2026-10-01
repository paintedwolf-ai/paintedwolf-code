package worker

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type panicExecutor struct{}

func (panicExecutor) Execute(ctx context.Context, task api.WorkerTask, run WorkerRunContext) (api.WorkerResult, error) {
	panic("boom in worker execution")
}

type serialProbeExecutor struct {
	starts  chan string
	release chan struct{}
}

func (e *serialProbeExecutor) Execute(_ context.Context, task api.WorkerTask, _ WorkerRunContext) (api.WorkerResult, error) {
	e.starts <- task.ID
	<-e.release
	return api.WorkerResult{Status: "complete"}, nil
}

func TestPollerClaimsCommittedEnqueueWithoutWaitingForMaintenance(t *testing.T) {
	cfg := DefaultWorkersConfig()
	cfg.Poller.MaxConcurrency = 1
	cfg.Poller.MaintenanceIntervalMs = int((time.Hour) / time.Millisecond)
	queue := NewInMemoryQueue(1)
	executor := &countingStartExecutor{}
	poller := NewLocalWorkerPoller(queue, executor, cfg, discardOutcomeRecorder{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()

	id, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt: "fixture", Brief: "fixture", ParentSessionID: "parent",
		ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue signaled worker", err)
	testutil.WaitFor(t, time.Second, func() bool {
		task, ok := queue.Get(id)
		return ok && task.Status == api.WorkerStatusComplete
	})
	if executor.calls.Load() != 1 {
		t.Fatalf("executor calls = %d want 1", executor.calls.Load())
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("poller stopped with %v", err)
	}
}

func TestPollerDrainsPendingWorkWhenCapacityReturns(t *testing.T) {
	cfg := DefaultWorkersConfig()
	cfg.Poller.MaxConcurrency = 1
	cfg.Poller.MaintenanceIntervalMs = int((time.Hour) / time.Millisecond)
	queue := NewInMemoryQueue(1)
	executor := &serialProbeExecutor{starts: make(chan string, 2), release: make(chan struct{}, 2)}
	poller := NewLocalWorkerPoller(queue, executor, cfg, discardOutcomeRecorder{})
	for i := 0; i < 2; i++ {
		_, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
			Prompt: "fixture", Brief: "fixture", ParentSessionID: "parent",
			ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal,
		})
		testutil.FailErr(t, "enqueue capacity worker", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()
	select {
	case <-executor.starts:
	case <-time.After(time.Second):
		t.Fatal("first worker did not start")
	}
	executor.release <- struct{}{}
	select {
	case <-executor.starts:
	case <-time.After(time.Second):
		t.Fatal("second worker waited for maintenance after capacity returned")
	}
	executor.release <- struct{}{}
	poller.inflight.Wait()
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("poller stopped with %v", err)
	}
}

// discardOutcomeRecorder isolates claim and execution tests from outcome delivery.
type discardOutcomeRecorder struct{}

func (discardOutcomeRecorder) OnWorkerComplete(context.Context, string, api.WorkerResult) error {
	return nil
}

func (discardOutcomeRecorder) OnWorkerFailed(context.Context, string, error) error { return nil }

func (discardOutcomeRecorder) OnOutcomeDelivered(context.Context, api.WorkerTask) {}

type flakyCompleteQueue struct {
	*InMemoryQueue
	attempts atomic.Int32
}

type flakyOutcomeRecorder struct {
	discardOutcomeRecorder
	attempts atomic.Int32
}

type cancelThenCompleteExecutor struct {
	cancel context.CancelFunc
}

func (e cancelThenCompleteExecutor) Execute(context.Context, api.WorkerTask, WorkerRunContext) (api.WorkerResult, error) {
	e.cancel()
	return api.WorkerResult{Status: "complete", Summary: "done"}, nil
}

type blockingOutcomeRecorder struct {
	discardOutcomeRecorder
	started     chan struct{}
	release     chan struct{}
	sawCanceled atomic.Bool
}

func (r *blockingOutcomeRecorder) OnWorkerComplete(ctx context.Context, _ string, _ api.WorkerResult) error {
	if ctx.Err() != nil {
		r.sawCanceled.Store(true)
	}
	close(r.started)
	<-r.release
	return nil
}

func (*blockingOutcomeRecorder) OnWorkerFailed(context.Context, string, error) error { return nil }

func (r *flakyOutcomeRecorder) OnWorkerComplete(context.Context, string, api.WorkerResult) error {
	if r.attempts.Add(1) == 1 {
		return errors.New("workflow store temporarily unavailable")
	}
	return nil
}

func (*flakyOutcomeRecorder) OnWorkerFailed(context.Context, string, error) error { return nil }

// stuckOutcomeRecorder rejects one job and records every attempt.
type stuckOutcomeRecorder struct {
	discardOutcomeRecorder
	mu     sync.Mutex
	stuck  string
	seen   []string
	failed int
}

func (r *stuckOutcomeRecorder) OnWorkerComplete(_ context.Context, jobID string, _ api.WorkerResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, jobID)
	if jobID == r.stuck {
		r.failed++
		return errors.New("workflow run revision conflict: run r expected revision 9, actual 10")
	}
	return nil
}

func (*stuckOutcomeRecorder) OnWorkerFailed(context.Context, string, error) error { return nil }

func (r *stuckOutcomeRecorder) sawJob(jobID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, seen := range r.seen {
		if seen == jobID {
			return true
		}
	}
	return false
}

func (q *flakyCompleteQueue) Complete(ctx context.Context, claimed *api.WorkerTask, result api.WorkerResult) (bool, error) {
	if q.attempts.Add(1) == 1 {
		return false, errors.New("database temporarily unavailable")
	}
	return q.InMemoryQueue.Complete(ctx, claimed, result)
}

// A worker panic fails only its claimed job.
func TestPollerRecoversExecutorPanic(t *testing.T) {
	cfg := DefaultWorkersConfig()
	cfg.Poller.MaxConcurrency = 1
	queue := NewInMemoryQueue(cfg.Poller.MaxConcurrency)
	queue.SetWorkersConfig(cfg)
	poller := NewLocalWorkerPoller(queue, panicExecutor{}, cfg, discardOutcomeRecorder{})

	ctx := context.Background()
	id, err := queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: "parent",
		ProjectID:       testdbseed.DefaultProjectID,
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "EnqueueWithProjectID", err)

	pollCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	go poller.Run(pollCtx)

	testutil.WaitFor(t, time.Second, func() bool {
		got, ok := queue.Get(id)
		return ok && got.Status == api.WorkerStatusFailed
	})

	got, ok := queue.Get(id)
	if !ok || got.Status != api.WorkerStatusFailed {
		t.Fatalf("panicking worker must end Failed, got ok=%v status=%q", ok, got.Status)
	}
}

func TestPollerRetriesTerminalCommit(t *testing.T) {
	base := NewInMemoryQueue(1)
	queue := &flakyCompleteQueue{InMemoryQueue: base}
	id, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: "parent",
		ProjectID:       testdbseed.DefaultProjectID,
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue worker", err)
	task, err := queue.ClaimNext(t.Context(), ClaimRequest{
		ProjectID:       testdbseed.DefaultProjectID,
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "claim worker", err)

	poller := NewLocalWorkerPoller(queue, nil, DefaultWorkersConfig(), discardOutcomeRecorder{})
	result := api.WorkerResult{Status: "complete", Summary: "done"}
	if won, _ := poller.commitTerminal(t.Context(), task, "completion", func() (bool, error) {
		return queue.Complete(t.Context(), task, result)
	}); !won {
		t.Fatal("terminal commit did not succeed")
	}

	got, ok := queue.Get(id)
	if !ok || got.Status != api.WorkerStatusComplete {
		t.Fatalf("worker after terminal retry = %+v want complete", got)
	}
	if attempts := queue.attempts.Load(); attempts != 2 {
		t.Fatalf("completion attempts = %d want 2", attempts)
	}
}

func TestPollerReplaysUnacknowledgedTerminalOutcome(t *testing.T) {
	queue := NewInMemoryQueue(1)
	id, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt: "fixture", Brief: "fixture", ParentSessionID: "parent",
		ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue worker", err)
	task, err := queue.ClaimNext(t.Context(), ClaimRequest{
		ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "claim worker", err)
	completed, err := queue.Complete(t.Context(), task, api.WorkerResult{Status: "complete"})
	testutil.FailErr(t, "complete worker", err)
	if !completed {
		t.Fatal("worker completion lost its claim")
	}

	recorder := &flakyOutcomeRecorder{}
	poller := NewLocalWorkerPoller(queue, panicExecutor{}, DefaultWorkersConfig(), recorder)
	if err := poller.deliverPendingOutcomes(t.Context()); err == nil {
		t.Fatal("first outcome delivery unexpectedly succeeded")
	}
	if err := poller.deliverPendingOutcomes(t.Context()); err != nil {
		t.Fatalf("replay terminal outcome: %v", err)
	}
	if err := poller.deliverPendingOutcomes(t.Context()); err != nil {
		t.Fatalf("read acknowledged outcomes: %v", err)
	}
	if got := recorder.attempts.Load(); got != 2 {
		t.Fatalf("outcome attempts for %s = %d want 2", id, got)
	}
}

func TestPollerDeliversTerminalOutcomeBeforeParentBecomesIdle(t *testing.T) {
	queue := NewInMemoryQueue(1)
	parent := "parent-outcome-order"
	_, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt: "fixture", Brief: "fixture", ParentSessionID: parent,
		ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue worker", err)
	task, err := queue.ClaimNext(t.Context(), ClaimRequest{
		ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "claim worker", err)

	parentCtx, cancelParent := context.WithCancel(t.Context())
	recorder := &blockingOutcomeRecorder{started: make(chan struct{}), release: make(chan struct{})}
	waiter := NewParentWorkerWaiter()
	poller := NewLocalWorkerPoller(queue, cancelThenCompleteExecutor{cancel: cancelParent}, DefaultWorkersConfig(), recorder)
	poller.ParentWaiter = waiter
	executeDone := make(chan struct{})
	go func() {
		poller.execute(parentCtx, task)
		close(executeDone)
	}()
	select {
	case <-recorder.started:
	case <-time.After(time.Second):
		t.Fatal("terminal outcome delivery did not start")
	}

	waitCtx, cancelWait := context.WithTimeout(t.Context(), time.Second)
	defer cancelWait()
	waitDone := make(chan error, 1)
	go func() {
		waitDone <- AwaitParentSessionWorkers(waitCtx, queue, waiter, testdbseed.DefaultProjectID, parent, settings.DefaultSessionLimits())
	}()
	select {
	case err := <-waitDone:
		t.Fatalf("parent became idle before terminal projection: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(recorder.release)
	select {
	case err := <-waitDone:
		testutil.FailErr(t, "await delivered outcome", err)
	case <-time.After(time.Second):
		t.Fatal("parent did not resume after terminal outcome acknowledgement")
	}
	<-executeDone
	if recorder.sawCanceled.Load() {
		t.Fatal("terminal outcome inherited the canceled worker execution context")
	}
}

// Delivery continues after an individual outcome fails.
func TestPollerDeliversOutcomesBehindAFailingOne(t *testing.T) {
	queue := NewInMemoryQueue(3)
	ids := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		id, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
			Prompt: "fixture", Brief: "fixture", ParentSessionID: "parent",
			ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal,
		})
		testutil.FailErr(t, "enqueue worker", err)
		task, err := queue.ClaimNext(t.Context(), ClaimRequest{
			ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal,
		})
		testutil.FailErr(t, "claim worker", err)
		completed, err := queue.Complete(t.Context(), task, api.WorkerResult{Status: "complete"})
		testutil.FailErr(t, "complete worker", err)
		if !completed {
			t.Fatal("worker completion lost its claim")
		}
		ids = append(ids, id)
	}

	recorder := &stuckOutcomeRecorder{stuck: ids[0]}
	poller := NewLocalWorkerPoller(queue, panicExecutor{}, DefaultWorkersConfig(), recorder)
	if err := poller.deliverPendingOutcomes(t.Context()); err == nil {
		t.Fatal("delivery must report the failing outcome")
	}
	for _, id := range ids[1:] {
		if !recorder.sawJob(id) {
			t.Fatalf("outcome for %s was never offered behind the failing one", id)
		}
	}
	pending, err := queue.ListPendingOutcomes(t.Context())
	testutil.FailErr(t, "list pending outcomes", err)
	if len(pending) != 1 || pending[0].ID != ids[0] {
		t.Fatalf("pending outcomes = %+v want only the stuck %s", pending, ids[0])
	}
}

func TestPollerKeepsRunningUntilDeferredOutcomeSucceeds(t *testing.T) {
	queue := NewInMemoryQueue(1)
	_, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt: "fixture", Brief: "fixture", ParentSessionID: "parent",
		ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue worker", err)
	task, err := queue.ClaimNext(t.Context(), ClaimRequest{
		ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "claim worker", err)
	completed, err := queue.Complete(t.Context(), task, api.WorkerResult{Status: "complete"})
	testutil.FailErr(t, "complete worker", err)
	if !completed {
		t.Fatal("worker completion lost its claim")
	}

	recorder := &flakyOutcomeRecorder{}
	cfg := DefaultWorkersConfig()
	cfg.Poller.MaintenanceIntervalMs = 5
	poller := NewLocalWorkerPoller(queue, panicExecutor{}, cfg, recorder)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()
	testutil.WaitFor(t, time.Second, func() bool { return recorder.attempts.Load() >= 2 })
	pending, err := queue.ListPendingOutcomes(t.Context())
	testutil.FailErr(t, "list acknowledged outcomes", err)
	if len(pending) != 0 {
		t.Fatalf("pending outcomes after retry = %+v", pending)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("poller stopped with %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("poller did not stop")
	}
}

func (panicExecutor) AbortWorkerRuntime(context.Context, api.WorkerTask) error { return nil }

func (*serialProbeExecutor) AbortWorkerRuntime(context.Context, api.WorkerTask) error { return nil }

func (cancelThenCompleteExecutor) AbortWorkerRuntime(context.Context, api.WorkerTask) error {
	return nil
}

type completeExecutor struct{}

func (completeExecutor) Execute(context.Context, api.WorkerTask, WorkerRunContext) (api.WorkerResult, error) {
	return api.WorkerResult{Status: "complete"}, nil
}

func (completeExecutor) AbortWorkerRuntime(context.Context, api.WorkerTask) error { return nil }

type failingCompleteQueue struct {
	*InMemoryQueue
	err error
}

func (q *failingCompleteQueue) Complete(context.Context, *api.WorkerTask, api.WorkerResult) (bool, error) {
	return false, q.err
}

func TestPollerTransitionsToRetryWhenCompletionCommitFailsWithAttemptsRemaining(t *testing.T) {
	base := NewInMemoryQueue(1)
	queue := &failingCompleteQueue{InMemoryQueue: base, err: errors.New("transient completion failure")}
	id, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt: "fixture", Brief: "fixture", ParentSessionID: "parent",
		ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue worker", err)

	cfg := DefaultWorkersConfig()
	poller := NewLocalWorkerPoller(queue, completeExecutor{}, cfg, discardOutcomeRecorder{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()

	testutil.WaitFor(t, 10*time.Second, func() bool {
		task, ok := queue.Get(id)
		return ok && task.Attempt >= 2
	})
	task, ok := queue.Get(id)
	if !ok || task.Attempt < 2 {
		t.Fatalf("task attempt = %d want >= 2", task.Attempt)
	}
	cancel()
	<-done
}

func TestPollerTransitionsToFailureWhenCompletionCommitFailsExhausted(t *testing.T) {
	base := NewInMemoryQueue(1)
	queue := &failingCompleteQueue{InMemoryQueue: base, err: errors.New("terminal completion failure")}
	id, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt: "fixture", Brief: "fixture", ParentSessionID: "parent",
		ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal,
		Attempt: 3,
	})
	testutil.FailErr(t, "enqueue worker", err)

	cfg := DefaultWorkersConfig()
	poller := NewLocalWorkerPoller(queue, completeExecutor{}, cfg, discardOutcomeRecorder{})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- poller.Run(ctx) }()

	testutil.WaitFor(t, 10*time.Second, func() bool {
		task, ok := queue.Get(id)
		return ok && task.Status == api.WorkerStatusFailed
	})
	task, ok := queue.Get(id)
	if !ok || task.Status != api.WorkerStatusFailed {
		t.Fatalf("task status = %v want failed", task.Status)
	}
	if !strings.Contains(task.Error, "terminal completion failure") {
		t.Fatalf("task error = %q want to contain 'terminal completion failure'", task.Error)
	}
	cancel()
	<-done
}
