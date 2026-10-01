package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/pkg/api"
)

func TestWorkerCancellationCoversEveryActiveState(t *testing.T) {
	for _, backend := range []string{"memory", "sql"} {
		for _, route := range []string{"job", "session", "workflow"} {
			if backend == "sql" && route == "workflow" {
				continue
			}
			for _, status := range []api.WorkerStatus{api.WorkerStatusPending, api.WorkerStatusHeld, api.WorkerStatusRunning, api.WorkerStatusWaiting} {
				t.Run(backend+"/"+route+"/"+string(status), func(t *testing.T) {
					var queue WorkerQueue
					interrupted := 0
					onCancel := func(context.Context, string) error { interrupted++; return nil }
					if backend == "sql" {
						database := testdbfixture.Open(t, "store.db")
						testdbseed.InsertSession(t, database, "parent-1", testdbseed.DefaultProjectID)
						testdbseed.InsertSession(t, database, "child-1", testdbseed.DefaultProjectID)
						q := NewSQLQueue(database, 1)
						q.SetRunningCancel(onCancel)
						queue = q
					} else {
						q := NewInMemoryQueue(1)
						q.SetWorkflowRunChecker(allowAllWorkflowRuns{})
						q.SetRunningCancel(onCancel)
						queue = q
					}
					runID := ""
					if route == "workflow" {
						runID = "run-1"
					}
					jobID, err := queue.Enqueue(t.Context(), api.WorkerTask{
						ParentSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID,
						WorkflowRunID: runID, AgentType: "implementer", Prompt: "bounded fixture", Brief: "fixture",
						ExecutionTarget: api.ExecutionTargetLocal,
					})
					testutil.FailErr(t, "enqueue cancellation fixture", err)
					testutil.FailErr(t, "bind cancellation child", queue.SetChildSessionID(t.Context(), jobID, "child-1"))
					switch status {
					case api.WorkerStatusHeld:
						testutil.FailErr(t, "hold cancellation fixture", queue.Hold(t.Context(), jobID))
					case api.WorkerStatusRunning, api.WorkerStatusWaiting:
						claimed, err := queue.ClaimNext(t.Context(), ClaimRequest{ClaimedBy: "fixture", ExecutionTarget: api.ExecutionTargetLocal})
						testutil.FailErr(t, "claim cancellation fixture", err)
						if status == api.WorkerStatusWaiting {
							won, err := queue.Park(t.Context(), claimed)
							testutil.FailErr(t, "park cancellation fixture", err)
							if !won {
								t.Fatal("fixture did not park")
							}
						}
					case api.WorkerStatusPending, api.WorkerStatusComplete, api.WorkerStatusFailed, api.WorkerStatusCanceled:
					}
					cancel := &CancelService{Queue: queue}
					switch route {
					case "job":
						_, err = cancel.CancelJob(t.Context(), jobID, "fixture canceled")
					case "session":
						err = cancel.AbortAllWorkers(t.Context(), "parent-1", testdbseed.DefaultProjectID, "fixture stopped")
					case "workflow":
						err = (&RunStopService{Queue: queue}).CancelWorkersByRunID(t.Context(), runID, "fixture stopped")
					}
					testutil.FailErr(t, "cancel active worker", err)
					testutil.FailErr(t, "repeat cancellation", queue.Cancel(t.Context(), jobID, nil))
					got, ok := queue.Get(jobID)
					if !ok || got.Status != api.WorkerStatusCanceled {
						t.Fatalf("worker after cancel = %+v", got)
					}
					// Retried and held jobs can retain a child runtime too.
					if interrupted != 1 {
						t.Fatalf("interrupts=%d want=1", interrupted)
					}
				})
			}
		}
	}
}

type parkedRuntimeExecutor struct {
	stopped string
	stopErr error
}

func (*parkedRuntimeExecutor) Execute(context.Context, api.WorkerTask, WorkerRunContext) (api.WorkerResult, error) {
	return api.WorkerResult{}, nil
}
func (e *parkedRuntimeExecutor) AbortWorkerRuntime(_ context.Context, task api.WorkerTask) error {
	e.stopped = task.ChildSessionID
	return e.stopErr
}

func TestPollerAbortReleasesParkedChildWithoutAnExecution(t *testing.T) {
	queue := NewInMemoryQueue(1)
	job, err := queue.Enqueue(t.Context(), api.WorkerTask{ParentSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID, AgentType: "implementer", Prompt: "fixture", Brief: "fixture"})
	testutil.FailErr(t, "enqueue parked cleanup", err)
	testutil.FailErr(t, "bind parked cleanup child", queue.SetChildSessionID(t.Context(), job, "child-1"))
	executor := &parkedRuntimeExecutor{}
	poller := NewLocalWorkerPoller(queue, executor, DefaultWorkersConfig(), discardOutcomeRecorder{})
	testutil.FailErr(t, "abort without live goroutine", poller.Abort(t.Context(), job))
	if executor.stopped != "child-1" {
		t.Fatalf("stopped child=%q", executor.stopped)
	}
}

func TestPollerAbortReportsParkedRuntimeFailure(t *testing.T) {
	queue := NewInMemoryQueue(1)
	job, err := queue.Enqueue(t.Context(), api.WorkerTask{ParentSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID, AgentType: "implementer", Prompt: "fixture", Brief: "fixture"})
	testutil.FailErr(t, "enqueue cleanup failure", err)
	testutil.FailErr(t, "bind cleanup failure child", queue.SetChildSessionID(t.Context(), job, "child-1"))
	cleanupErr := errors.New("cleanup failed")
	executor := &parkedRuntimeExecutor{stopErr: cleanupErr}
	poller := NewLocalWorkerPoller(queue, executor, DefaultWorkersConfig(), discardOutcomeRecorder{})
	if err := poller.Abort(t.Context(), job); !errors.Is(err, cleanupErr) {
		t.Fatalf("abort error = %v, want %v", err, cleanupErr)
	}
	if executor.stopped != "child-1" {
		t.Fatalf("stopped child = %q", executor.stopped)
	}
}

func TestPollerAbortRequiresExecutorForRetainedTask(t *testing.T) {
	queue := NewInMemoryQueue(1)
	job, err := queue.Enqueue(t.Context(), api.WorkerTask{ParentSessionID: "parent-1", ProjectID: testdbseed.DefaultProjectID, AgentType: "implementer", Prompt: "fixture", Brief: "fixture"})
	testutil.FailErr(t, "enqueue missing executor", err)
	poller := NewLocalWorkerPoller(queue, nil, DefaultWorkersConfig(), discardOutcomeRecorder{})
	if err := poller.Abort(t.Context(), job); err == nil {
		t.Fatal("cleanup succeeded without an executor")
	}
	testutil.FailErr(t, "abort absent task", poller.Abort(t.Context(), "absent"))
}
