package worker

import (
	"context"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

// notifyDuringCheckQueue fires during the first active-job check.
type notifyDuringCheckQueue struct {
	WorkerQueue
	waiter          *ParentWorkerWaiter
	parentSessionID string
	calls           int
}

func (q *notifyDuringCheckQueue) ListBySession(_ context.Context, _, _ string, _ ...api.WorkerStatus) ([]api.WorkerTask, error) {
	q.calls++
	if q.calls == 1 {
		// Complete while the waiter is registered.
		q.waiter.NotifyParent(q.parentSessionID)
		return []api.WorkerTask{{ID: "job-1", Status: api.WorkerStatusRunning}}, nil
	}
	return nil, nil
}

func (*notifyDuringCheckQueue) ListPendingOutcomes(context.Context) ([]api.WorkerTask, error) {
	return nil, nil
}

func TestAwaitParentSessionWorkersDoesNotMissWakeupBetweenCheckAndRegister(t *testing.T) {
	waiter := NewParentWorkerWaiter()
	queue := &notifyDuringCheckQueue{waiter: waiter, parentSessionID: "parent-1"}
	lim := settings.DefaultSessionLimits()
	lim.AwaitParentWorkersTimeoutSec = 1

	start := time.Now()
	err := AwaitParentSessionWorkers(t.Context(), queue, waiter, testdbseed.DefaultProjectID, "parent-1", lim)
	elapsed := time.Since(start)
	testutil.FailErr(t, "await parent session workers", err)

	if elapsed >= lim.AwaitParentWorkersTimeout()/2 {
		t.Fatalf("await took %v, want well under timeout %v (wakeup was missed)", elapsed, lim.AwaitParentWorkersTimeout())
	}
	if queue.calls != 2 {
		t.Fatalf("ListBySession calls = %d, want 2 (initial check + recheck after wake)", queue.calls)
	}
}
