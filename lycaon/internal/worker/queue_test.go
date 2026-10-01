package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func localClaim(ctx context.Context, q *InMemoryQueue, projectDir string) (*api.WorkerTask, error) {
	return q.ClaimNext(ctx, ClaimRequest{
		ProjectID:       testdbseed.DefaultProjectID,
		ExecutionTarget: api.ExecutionTargetLocal,
		ClaimedBy:       "test-claimer",
	})
}

func TestGetLatestByChildSessionIDReturnsNewestRun(t *testing.T) {
	q := NewInMemoryQueue(2)
	created := time.Now().UTC()
	for _, task := range []api.WorkerTask{
		{ID: "job-old", ChildSessionID: "child-1", CreatedAt: created},
		{ID: "job-new", ChildSessionID: "child-1", CreatedAt: created.Add(time.Second)},
	} {
		task.ParentSessionID = "parent"
		task.ProjectID = testdbseed.DefaultProjectID
		task.Prompt = "fixture"
		task.Brief = "fixture"
		task.ExecutionTarget = api.ExecutionTargetLocal
		_, err := q.EnqueueWithProjectID(t.Context(), task.ProjectID, task)
		testutil.FailErr(t, "enqueue worker run", err)
	}

	got, ok := q.GetLatestByChildSessionID(t.Context(), "child-1")
	if !ok || got.ID != "job-new" {
		t.Fatalf("worker run = %+v, %v", got, ok)
	}
}

func TestQueueLifecycle(t *testing.T) {
	q := NewInMemoryQueue(10)
	ctx := context.Background()

	id, err := q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		ParentSessionID: "sess-1",
		Prompt:          "do work",
		Brief:           "fixture",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "q.EnqueueWithProjectID failed", err)

	claimed, err := localClaim(ctx, q, "/tmp/proj")
	testutil.FailErr(t, "localClaim failed", err)
	if claimed.ID != id {
		t.Fatalf("id = %q want %q", claimed.ID, id)
	}
	if claimed.Status != api.WorkerStatusRunning {
		t.Fatalf("status = %q", claimed.Status)
	}
	if claimed.ClaimedBy != "test-claimer" {
		t.Fatalf("claimed_by = %q", claimed.ClaimedBy)
	}
	stale := *claimed
	stale.ClaimToken = "stale"
	if won, err := q.Complete(ctx, &stale, api.WorkerResult{Summary: "stale"}); err != nil {
		testutil.FailErr(t, "stale completion", err)
	} else if won {
		t.Fatal("stale completion won")
	}
	if current, ok := q.Get(id); !ok || current.Status != api.WorkerStatusRunning {
		t.Fatalf("task after stale completion = %+v", current)
	}

	if won, err := q.Complete(ctx, claimed, api.WorkerResult{Summary: "done"}); err != nil {
		testutil.FailErr(t, "q.Complete failed", err)
	} else if !won {
		t.Fatal("current completion claim lost")
	}
	got, ok := q.Get(id)
	if !ok || got.Status != api.WorkerStatusComplete {
		t.Fatalf("task = %+v", got)
	}
}

func TestQueueMaxWorkers(t *testing.T) {
	q := NewInMemoryQueue(2)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		_, err := q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
			Prompt:          "fixture",
			Brief:           "fixture",
			ParentSessionID: "s",
			ProjectID:       testdbseed.DefaultProjectID,
			ExecutionTarget: api.ExecutionTargetLocal,
		})
		testutil.FailErr(t, "q.EnqueueWithProjectID failed", err)
	}

	if _, err := localClaim(ctx, q, ""); err != nil {
		testutil.FailErr(t, "localClaim failed", err)
	}
	if _, err := localClaim(ctx, q, ""); err != nil {
		testutil.FailErr(t, "localClaim failed", err)
	}
	if _, err := localClaim(ctx, q, ""); !errors.Is(err, ErrMaxWorkers) {
		t.Fatalf("err = %v", err)
	}
}

func TestQueueCancel(t *testing.T) {
	q := NewInMemoryQueue(10)
	ctx := context.Background()

	id, _ := q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: "s",
		ProjectID:       testdbseed.DefaultProjectID,
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	_, _ = localClaim(ctx, q, "")

	if err := q.Cancel(ctx, id, nil); err != nil {
		testutil.FailErr(t, "q.Cancel failed", err)
	}
	got, _ := q.Get(id)
	if got.Status != api.WorkerStatusCanceled {
		t.Fatalf("status = %q", got.Status)
	}
}

func TestConcurrentWorkerCap(t *testing.T) {
	q := NewInMemoryQueue(5)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		_, _ = q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
			Prompt:          "fixture",
			Brief:           "fixture",
			ParentSessionID: "s",
			ProjectID:       testdbseed.DefaultProjectID,
			ExecutionTarget: api.ExecutionTargetLocal,
		})
	}

	var wg sync.WaitGroup
	claimed := make(chan string, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			task, err := localClaim(ctx, q, "")
			if err == nil {
				claimed <- task.ID
			}
		}()
	}
	wg.Wait()
	close(claimed)

	var count int
	for range claimed {
		count++
	}
	if count != 5 {
		t.Fatalf("claimed %d, want 5", count)
	}
}

func TestRunnerTargetNotClaimedLocally(t *testing.T) {
	q := NewInMemoryQueue(10)
	ctx := context.Background()

	_, err := q.EnqueueWithProjectID(ctx, testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt:          "fixture",
		Brief:           "fixture",
		ParentSessionID: "s",
		ProjectID:       testdbseed.DefaultProjectID,
		ExecutionTarget: api.ExecutionTargetRunner,
		RunnerID:        "runner-a",
	})
	testutil.FailErr(t, "q.EnqueueWithProjectID failed", err)

	if _, err := localClaim(ctx, q, ""); !errors.Is(err, ErrNoPendingJobs) {
		t.Fatalf("err = %v", err)
	}
}
