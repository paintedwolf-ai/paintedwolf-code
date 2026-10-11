package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/session/workeroutcomes"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type cycleOutcomeRecorder struct {
	discardOutcomeRecorder
	queue WorkerQueue
	ready atomic.Int32
}

func (r *cycleOutcomeRecorder) OnWorkerComplete(ctx context.Context, jobID string, _ api.WorkerResult) error {
	// Let peer deliveries contend while this projection is in flight.
	time.Sleep(time.Millisecond)
	idle, err := workeroutcomes.ParentSessionWorkerCycleIdle(ctx, r.queue, testdbseed.DefaultProjectID, "parent", jobID)
	if idle {
		r.ready.Add(1)
	}
	return err
}

func TestConcurrentOutcomesSettleParentCycle(t *testing.T) {
	ctx := t.Context()
	queue := NewInMemoryQueue(32)
	var tasks []api.WorkerTask
	for range 16 {
		id, err := queue.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{Prompt: "fixture", Brief: "fixture", ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal})
		testutil.FailErr(t, "enqueue worker", err)
		claimed, err := queue.ClaimNext(ctx, ClaimRequest{ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal})
		testutil.FailErr(t, "claim worker", err)
		won, err := queue.Complete(ctx, claimed, api.WorkerResult{Status: "complete"})
		testutil.FailErr(t, "complete worker", err)
		if !won {
			t.Fatal("lost worker claim")
		}
		task, _ := queue.Get(id)
		tasks = append(tasks, *task)
	}
	recorder := &cycleOutcomeRecorder{queue: queue}
	poller := NewLocalWorkerPoller(queue, panicExecutor{}, DefaultWorkersConfig(), recorder)
	var group sync.WaitGroup
	failures := make(chan error, len(tasks))
	start := make(chan struct{})
	for _, task := range tasks {
		group.Go(func() { <-start; failures <- poller.deliverOutcome(ctx, task) })
	}
	close(start)
	group.Wait()
	close(failures)
	for err := range failures {
		testutil.FailErr(t, "deliver worker outcome", err)
	}
	if got := recorder.ready.Load(); got != 1 {
		t.Fatalf("ready cycles=%d, want exactly one", got)
	}
	pending, err := queue.ListPendingOutcomes(ctx)
	testutil.FailErr(t, "read pending outcomes", err)
	if len(pending) != 0 {
		t.Fatal("terminal results remain unacknowledged")
	}
}
