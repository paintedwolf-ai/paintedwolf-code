package worker

import (
	"errors"
	"testing"
	"time"

	"github.com/lycaon/lycaon/internal/db"
	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkflowCancellationPreservesClaimsUntilRuntimeSettlement(t *testing.T) {
	for _, scope := range []string{"all", "running"} {
		t.Run(scope, func(t *testing.T) {
			database := testdbfixture.Open(t, "store.db")
			testdbseed.InsertWorkflowRun(t, database, "run", "parent", testdbseed.DefaultProjectID)
			q := NewSQLQueue(database, 4)
			q.SetWorkflowRunChecker(allowAllWorkflowRuns{})
			ids := make([]string, 4)
			for i := range ids {
				id, err := q.Enqueue(t.Context(), api.WorkerTask{
					ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID, WorkflowRunID: "run",
					AgentType: "implementer", Prompt: "fixture", Brief: "fixture",
				})
				testutil.FailErr(t, "enqueue workflow worker", err)
				ids[i] = id
			}
			running := claimCancellationWorker(t, q)
			waiting := claimCancellationWorker(t, q)
			_, err := q.Park(t.Context(), waiting)
			testutil.FailErr(t, "park workflow worker", err)
			testutil.FailErr(t, "hold workflow worker", q.Hold(t.Context(), ids[2]))
			queries := db.New(database)
			if scope == "all" {
				err = queries.RequestWorkflowWorkerCancellation(t.Context(), db.RequestWorkflowWorkerCancellationParams{
					WorkflowRunID: db.NullString("run"), RequestedAt: db.NullString(db.FormatTime(time.Now().UTC())),
				})
			} else {
				err = queries.RequestRunningWorkflowWorkerCancellation(t.Context(), db.RequestRunningWorkflowWorkerCancellationParams{
					WorkflowRunID: db.NullString("run"), RequestedAt: db.NullString(db.FormatTime(time.Now().UTC())),
				})
			}
			testutil.FailErr(t, "commit workflow stop intent", err)
			got, _ := q.Get(running.ID)
			if got.Status != api.WorkerStatusRunning || got.ClaimToken != running.ClaimToken || got.Result != nil {
				t.Fatalf("workflow prematurely settled running claim: %+v", got)
			}
			won, err := q.Retry(t.Context(), running, errors.New("late provider response"))
			testutil.FailErr(t, "reject retry after workflow stop", err)
			if won {
				t.Fatal("workflow stop allowed retry")
			}
			testutil.FailErr(t, "resume held workflow workers", queries.ReleaseWorkflowWorkers(t.Context(), db.NullString("run")))
			if scope == "running" {
				// A resumed worker is outside the earlier pause's cancellation set.
				claimCancellationWorker(t, q)
			}
			testutil.FailErr(t, "settle workflow cancellation", (&RunStopService{Queue: q}).SettleWorkerCancellationsByRunID(t.Context(), "run", "workflow stopped"))
			for i, id := range ids {
				got, _ := q.Get(id)
				wantCanceled := scope == "all" || i < 2
				if (got.Status == api.WorkerStatusCanceled) != wantCanceled {
					t.Fatalf("worker %d scope=%s task=%+v", i, scope, got)
				}
			}
			var attemptStatus string
			testutil.FailErr(t, "inspect canceled attempt", database.QueryRowContext(t.Context(), "SELECT status FROM worker_attempts WHERE claim_token = ?", running.ClaimToken).Scan(&attemptStatus))
			if attemptStatus != "canceled" {
				t.Fatalf("workflow left attempt %q", attemptStatus)
			}
		})
	}
}
