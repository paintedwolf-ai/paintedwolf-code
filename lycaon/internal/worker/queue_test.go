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

func localClaim(ctx context.Context, q *SQLQueue) (*api.WorkerTask, error) {
	return q.ClaimNext(ctx, ClaimRequest{
		ProjectID:       testdbseed.DefaultProjectID,
		ExecutionTarget: api.ExecutionTargetLocal,
		ClaimedBy:       "test-claimer",
	})
}

func TestGetLatestByChildSessionIDReturnsNewestRun(t *testing.T) {
	q := newTestQueue(t, 2, testdbseed.DefaultProjectID, "parent", "child-1")
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

func TestConcurrentWorkerCap(t *testing.T) {
	q := newTestQueue(t, 5, testdbseed.DefaultProjectID, "s")
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
			task, err := localClaim(ctx, q)
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
	q := newTestQueue(t, 10, testdbseed.DefaultProjectID, "s")
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

	if _, err := localClaim(ctx, q); !errors.Is(err, ErrNoPendingJobs) {
		t.Fatalf("err = %v", err)
	}
}
