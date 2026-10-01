package worker_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/pagecursor"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestSessionWorkerPagesPreserveScopeAndHistoryBoundary(t *testing.T) {
	for name, create := range matrixQueueBackends {
		t.Run(name, func(t *testing.T) {
			queue := create(t)
			enqueue := func(created time.Time) string {
				id, err := queue.Enqueue(t.Context(), api.WorkerTask{
					ParentSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID, AgentType: "implementer",
					Prompt: "page fixture", Brief: "fixture", ExecutionTarget: api.ExecutionTargetLocal, CreatedAt: created,
				})
				testutil.FailErr(t, "enqueue worker", err)
				return id
			}
			created := time.Now().UTC().Add(-time.Minute)
			expected := map[string]bool{}
			for range 5 {
				expected[enqueue(created)] = true
			}
			query := worker.SessionPageQuery{ProjectID: testdbseed.DefaultProjectID, SessionID: "parent-1", Limit: 2}
			first, err := queue.ListSessionPage(t.Context(), query)
			testutil.FailErr(t, "first worker page", err)
			if len(first.Workers) != 2 || first.NextCursor == "" {
				t.Fatalf("first page = %+v", first)
			}
			enqueue(time.Now().UTC().Add(time.Minute))
			seen := map[string]bool{}
			page := first
			for {
				for _, task := range page.Workers {
					if seen[task.ID] || !expected[task.ID] {
						t.Fatalf("unexpected or repeated worker %s", task.ID)
					}
					seen[task.ID] = true
				}
				if page.NextCursor == "" {
					break
				}
				query.Cursor = page.NextCursor
				page, err = queue.ListSessionPage(t.Context(), query)
				testutil.FailErr(t, "next worker page", err)
			}
			if len(seen) != len(expected) {
				t.Fatalf("read %d workers, want %d", len(seen), len(expected))
			}
			for _, scope := range []worker.SessionPageQuery{
				{ProjectID: "different", SessionID: query.SessionID, Limit: 2, Cursor: first.NextCursor},
				{ProjectID: query.ProjectID, SessionID: "different", Limit: 2, Cursor: first.NextCursor},
				{ProjectID: query.ProjectID, SessionID: query.SessionID, Status: api.WorkerStatusPending, Limit: 2, Cursor: first.NextCursor},
			} {
				_, err := queue.ListSessionPage(t.Context(), scope)
				if !errors.Is(err, pagecursor.ErrInvalid) {
					t.Fatalf("foreign cursor error=%v", err)
				}
			}
		})
	}
}

func TestSessionWorkerPageLimitContract(t *testing.T) {
	for name, create := range matrixQueueBackends {
		t.Run(name, func(t *testing.T) {
			queue := create(t)
			_, err := queue.Enqueue(t.Context(), api.WorkerTask{
				ParentSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID, AgentType: "implementer",
				Prompt: "page fixture", Brief: "fixture", ExecutionTarget: api.ExecutionTargetLocal,
			})
			testutil.FailErr(t, "enqueue worker", err)
			for _, limit := range []int{-1, 0, worker.MaxSessionPageSize + 1, int(^uint(0) >> 1)} {
				_, err := queue.ListSessionPage(t.Context(), worker.SessionPageQuery{
					ProjectID: testdbseed.DefaultProjectID, SessionID: "parent-1", Limit: limit,
				})
				if !errors.Is(err, worker.ErrInvalidPageLimit) {
					t.Fatalf("limit %d: error=%v", limit, err)
				}
			}
			for _, limit := range []int{1, worker.MaxSessionPageSize} {
				page, err := queue.ListSessionPage(t.Context(), worker.SessionPageQuery{
					ProjectID: testdbseed.DefaultProjectID, SessionID: "parent-1", Limit: limit,
				})
				testutil.FailErr(t, "valid boundary worker page", err)
				if len(page.Workers) != 1 || page.NextCursor != "" {
					t.Fatalf("limit %d: page=%+v", limit, page)
				}
			}
		})
	}
}
