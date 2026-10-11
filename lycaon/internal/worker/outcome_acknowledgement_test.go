package worker

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

type cancellationOutcomeRecorder struct {
	discardOutcomeRecorder
	results  []api.WorkerResult
	failures int
}

func (r *cancellationOutcomeRecorder) OnWorkerComplete(_ context.Context, _ string, result api.WorkerResult) error {
	r.results = append(r.results, result)
	return nil
}

func (r *cancellationOutcomeRecorder) OnWorkerFailed(context.Context, string, error) error {
	r.failures++
	return nil
}

func TestCanceledOutcomeRetainsItsResultAcrossDeliveryRetry(t *testing.T) {
	for _, hasReport := range []bool{false, true} {
		t.Run(map[bool]string{false: "host-only", true: "change-report"}[hasReport], func(t *testing.T) {
			queue := &acknowledgementQueue{InMemoryQueue: NewInMemoryQueue(1), ackError: errors.New("acknowledgement unavailable")}
			id, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
				Prompt: "fixture", Brief: "fixture", ParentSessionID: "parent", ProjectID: testdbseed.DefaultProjectID,
			})
			testutil.FailErr(t, "enqueue cancellation", err)
			want := api.WorkerResult{Status: "canceled", Response: "canceled"}
			var result *api.WorkerResult
			if hasReport {
				want.Summary = "Stopped with one unpromoted change"
				want.ChangeReport = &api.WorkerChangeReport{ChangedPaths: []string{"README.md"}, ReceiptCount: 1}
				result = &want
			}
			testutil.FailErr(t, "cancel worker", queue.Cancel(t.Context(), id, result))
			task, ok := queue.Get(id)
			if !ok || task.Result == nil {
				t.Fatal("canceled worker missing")
			}
			if !hasReport {
				report := task.Result.ChangeReport
				if task.Result.Summary == "" || report == nil || report.WorkspaceDirty || len(report.ChangedPaths) != 0 {
					t.Fatalf("host cancellation report=%+v", task.Result)
				}
				want.Summary = task.Result.Summary
				want.ChangeReport = report
			}
			recorder := &cancellationOutcomeRecorder{}
			poller := NewLocalWorkerPoller(queue, panicExecutor{}, DefaultWorkersConfig(), recorder)
			if err := poller.deliverOutcome(t.Context(), *task); !errors.Is(err, queue.ackError) {
				t.Fatalf("delivery error=%v", err)
			}
			queue.ackError = nil
			testutil.FailErr(t, "retry canceled outcome", poller.deliverOutcome(t.Context(), *task))
			if recorder.failures != 0 || len(recorder.results) != 2 {
				t.Fatalf("results=%d failures=%d", len(recorder.results), recorder.failures)
			}
			for _, got := range recorder.results {
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("canceled outcome=%+v want=%+v", got, want)
				}
			}
		})
	}
}

type acknowledgementQueue struct {
	*InMemoryQueue
	ackError error
}

func (q *acknowledgementQueue) MarkOutcomeDelivered(ctx context.Context, id string) error {
	if q.ackError != nil {
		return q.ackError
	}
	return q.InMemoryQueue.MarkOutcomeDelivered(ctx, id)
}

type acknowledgementRecorder struct {
	discardOutcomeRecorder
	queue     WorkerQueue
	released  int
	pending   int
	readError error
}

func (r *acknowledgementRecorder) OnOutcomeDelivered(ctx context.Context, _ api.WorkerTask) {
	items, err := r.queue.ListPendingOutcomes(ctx)
	r.pending, r.readError = len(items), err
	r.released++
}

func TestParentReleaseFollowsCommittedOutcomeAcknowledgement(t *testing.T) {
	queue := &acknowledgementQueue{InMemoryQueue: NewInMemoryQueue(1), ackError: errors.New("acknowledgement unavailable")}
	id, err := queue.EnqueueWithProjectID(t.Context(), testdbseed.DefaultProjectID, api.WorkerTask{
		Prompt: "fixture", Brief: "fixture", ParentSessionID: "parent",
		ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue worker", err)
	claimed, err := queue.ClaimNext(t.Context(), ClaimRequest{
		ProjectID: testdbseed.DefaultProjectID, ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "claim worker", err)
	completed, err := queue.Complete(t.Context(), claimed, api.WorkerResult{Status: "complete"})
	testutil.FailErr(t, "complete worker", err)
	if !completed {
		t.Fatal("worker completion lost its claim")
	}
	task, ok := queue.Get(id)
	if !ok {
		t.Fatal("completed worker missing")
	}
	recorder := &acknowledgementRecorder{queue: queue}
	poller := NewLocalWorkerPoller(queue, panicExecutor{}, DefaultWorkersConfig(), recorder)
	if err := poller.deliverOutcome(t.Context(), *task); !errors.Is(err, queue.ackError) {
		t.Fatalf("delivery error = %v, want acknowledgement failure", err)
	}
	if recorder.released != 0 {
		t.Fatal("parent released before acknowledgement committed")
	}
	queue.ackError = nil
	testutil.FailErr(t, "retry acknowledgement", poller.deliverOutcome(t.Context(), *task))
	testutil.FailErr(t, "read outcomes at release", recorder.readError)
	if recorder.released != 1 || recorder.pending != 0 {
		t.Fatalf("parent release: calls=%d pending outcomes=%d", recorder.released, recorder.pending)
	}
}

func TestSessionBridgeReleasesCycleOnlyAfterDeliveryAcknowledgement(t *testing.T) {
	for _, status := range []api.WorkerStatus{api.WorkerStatusComplete, api.WorkerStatusFailed, api.WorkerStatusCanceled} {
		t.Run(string(status), func(t *testing.T) {
			sessions := &proofCountSessions{shouldNudge: true}
			bridge := &SessionOutcomeBridge{Workers: sessions, Loop: sessions, Results: sessions, State: sessions, Closure: sessions}
			var err error
			switch status {
			case api.WorkerStatusComplete:
				err = bridge.OnWorkerComplete(t.Context(), "job", api.WorkerResult{Status: "complete"})
			case api.WorkerStatusFailed:
				err = bridge.OnWorkerFailed(t.Context(), "job", errors.New("worker failed"))
			case api.WorkerStatusCanceled:
				err = bridge.OnWorkerComplete(t.Context(), "job", api.WorkerResult{Status: "canceled"})
			case api.WorkerStatusPending, api.WorkerStatusRunning, api.WorkerStatusWaiting, api.WorkerStatusHeld:
			}
			testutil.FailErr(t, "project worker terminal", err)
			if sessions.terminalCalls.Load() != 0 {
				t.Fatal("projection released a cycle before delivery acknowledgement")
			}
			bridge.OnOutcomeDelivered(t.Context(), api.WorkerTask{ID: "job", ParentSessionID: "parent-1", Status: status})
			if sessions.terminalCalls.Load() != 1 {
				t.Fatal("delivery acknowledgement did not release the parent cycle")
			}
		})
	}
}
