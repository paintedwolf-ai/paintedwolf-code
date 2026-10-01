package worker

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/settings"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestAwaitParentSessionWorkersReturnsWhenIdle(t *testing.T) {
	q := NewInMemoryQueue(4)
	waiter := NewParentWorkerWaiter()
	ctx := context.Background()
	parent := "parent-1"
	jobID, err := q.Enqueue(ctx, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: parent,
		ProjectID:       testdbseed.DefaultProjectID,
		AgentType:       "implementer",
		Status:          api.WorkerStatusPending,
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "q.Enqueue failed", err)
	task, err := q.ClaimNext(ctx, ClaimRequest{ClaimedBy: "test", ExecutionTarget: api.ExecutionTargetLocal})
	testutil.FailErr(t, "q.ClaimNext failed", err)
	if task.ID != jobID {
		t.Fatalf("claimed %q want %q", task.ID, jobID)
	}
	done := make(chan struct{})
	go func() {
		_ = AwaitParentSessionWorkers(ctx, q, waiter, testdbseed.DefaultProjectID, parent, settings.DefaultSessionLimits())
		close(done)
	}()
	time.Sleep(50 * time.Millisecond)
	if won, err := q.Complete(ctx, task, api.WorkerResult{Status: "complete"}); err != nil || !won {
		testutil.FailErr(t, "q.Complete failed", err)
		if !won {
			t.Fatal("completion claim lost")
		}
	}
	testutil.FailErr(t, "mark outcome delivered", q.MarkOutcomeDelivered(ctx, task.ID))
	waiter.NotifyParent(parent)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("await did not return after job completed")
	}
}

func TestAwaitParentSessionWorkersIgnoresOtherSessions(t *testing.T) {
	q := NewInMemoryQueue(4)
	ctx := context.Background()
	_, err := q.Enqueue(ctx, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: "other",
		ProjectID:       testdbseed.DefaultProjectID,
		AgentType:       "implementer",
		Status:          api.WorkerStatusPending,
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "q.Enqueue failed", err)
	if err := AwaitParentSessionWorkers(ctx, q, NewParentWorkerWaiter(), testdbseed.DefaultProjectID, "parent-1", settings.DefaultSessionLimits()); err != nil {
		t.Fatalf("other session jobs must not block parent await: %v", err)
	}
}

func TestAwaitParentSessionWorkersWaitsForRunning(t *testing.T) {
	q := NewInMemoryQueue(4)
	waiter := NewParentWorkerWaiter()
	ctx := context.Background()
	parent := "parent-1"
	jobID, err := q.Enqueue(ctx, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: parent,
		ProjectID:       testdbseed.DefaultProjectID,
		AgentType:       "implementer",
		Status:          api.WorkerStatusPending,
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "q.Enqueue failed", err)
	task, err := q.ClaimNext(ctx, ClaimRequest{
		ProjectID:       testdbseed.DefaultProjectID,
		ClaimedBy:       "test",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	if err != nil || task == nil {
		t.Fatalf("claim: %v task=%v", err, task)
	}
	if task.ID != jobID {
		t.Fatalf("claimed %q want %q", task.ID, jobID)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_ = AwaitParentSessionWorkers(ctx, q, waiter, testdbseed.DefaultProjectID, parent, settings.DefaultSessionLimits())
	}()
	time.Sleep(80 * time.Millisecond)
	if won, err := q.Complete(ctx, task, api.WorkerResult{Status: "complete"}); err != nil || !won {
		testutil.FailErr(t, "q.Complete failed", err)
		if !won {
			t.Fatal("completion claim lost")
		}
	}
	testutil.FailErr(t, "mark outcome delivered", q.MarkOutcomeDelivered(ctx, task.ID))
	waiter.NotifyParent(parent)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("await did not return after running job completed")
	}
}
