package worker_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/lycaon/lycaon/internal/testdbfixture"
	"github.com/lycaon/lycaon/internal/testdbseed"
	"github.com/lycaon/lycaon/internal/testutil"
	"github.com/lycaon/lycaon/internal/worker"
	"github.com/lycaon/lycaon/pkg/api"
)

type allowAllRuns struct{}

func (allowAllRuns) AssertRunnable(context.Context, string) error { return nil }

func (allowAllRuns) AssertWorkerTask(context.Context, *api.WorkerTask) error { return nil }

var matrixQueueBackends = map[string]func(t *testing.T) worker.WorkerQueue{
	"memory": func(*testing.T) worker.WorkerQueue {
		q := worker.NewInMemoryQueue(1)
		q.SetWorkflowDomains(&worker.WorkflowDomains{Runs: allowAllRuns{}, Tasks: allowAllRuns{}})
		return q
	},
	"sql": func(t *testing.T) worker.WorkerQueue {
		database := testdbfixture.Open(t, "matrix.db")
		testdbseed.InsertSession(t, database, "parent-1", testdbseed.DefaultProjectID)
		testdbseed.InsertSession(t, database, "child-1", testdbseed.DefaultProjectID)
		return worker.NewSQLQueue(database, 1)
	},
}

var matrixClaim = worker.ClaimRequest{ClaimedBy: "fixture", ExecutionTarget: api.ExecutionTargetLocal}

// queueStatusTransition invokes one WorkerQueue method against the seeded job.
type queueStatusTransition func(ctx context.Context, q worker.WorkerQueue, jobID string, task *api.WorkerTask) (bool, error)

var queueStatusTransitions = map[string]queueStatusTransition{
	"ClaimNext": func(ctx context.Context, q worker.WorkerQueue, _ string, _ *api.WorkerTask) (bool, error) {
		claimed, err := q.ClaimNext(ctx, matrixClaim)
		return claimed != nil, err
	},
	"Complete": func(ctx context.Context, q worker.WorkerQueue, _ string, task *api.WorkerTask) (bool, error) {
		return q.Complete(ctx, task, api.WorkerResult{Status: "complete"})
	},
	"Retry": func(ctx context.Context, q worker.WorkerQueue, _ string, task *api.WorkerTask) (bool, error) {
		return q.Retry(ctx, task, errors.New("retry error"))
	},
	"Park": func(ctx context.Context, q worker.WorkerQueue, _ string, task *api.WorkerTask) (bool, error) {
		return q.Park(ctx, task)
	},
	"ResumeReadyWaits": func(ctx context.Context, q worker.WorkerQueue, _ string, _ *api.WorkerTask) (bool, error) {
		n, err := q.ResumeReadyWaits(ctx)
		return n > 0, err
	},
	"Fail": func(ctx context.Context, q worker.WorkerQueue, _ string, task *api.WorkerTask) (bool, error) {
		return q.Fail(ctx, task, errors.New("fail error"))
	},
	"RequestCancellation": func(ctx context.Context, q worker.WorkerQueue, jobID string, _ *api.WorkerTask) (bool, error) {
		return q.RequestCancellation(ctx, jobID)
	},
	"RequestClaimCancellation": func(ctx context.Context, q worker.WorkerQueue, _ string, task *api.WorkerTask) (bool, error) {
		return q.RequestClaimCancellation(ctx, task)
	},
	"StopExecution": func(ctx context.Context, q worker.WorkerQueue, jobID string, _ *api.WorkerTask) (bool, error) {
		return false, q.StopExecution(ctx, jobID)
	},
	"Cancel": func(ctx context.Context, q worker.WorkerQueue, jobID string, _ *api.WorkerTask) (bool, error) {
		return false, q.Cancel(ctx, jobID, &api.WorkerResult{Status: "canceled"})
	},
	"FinishCanceled": func(ctx context.Context, q worker.WorkerQueue, jobID string, _ *api.WorkerTask) (bool, error) {
		return false, q.FinishCanceled(ctx, jobID, &api.WorkerResult{Status: "canceled"})
	},
	"Hold": func(ctx context.Context, q worker.WorkerQueue, jobID string, _ *api.WorkerTask) (bool, error) {
		return false, q.Hold(ctx, jobID)
	},
	"RecoverExpiredClaims": func(ctx context.Context, q worker.WorkerQueue, _ string, _ *api.WorkerTask) (bool, error) {
		recovered, err := q.RecoverExpiredClaims(ctx)
		return len(recovered) > 0, err
	},
}

var queueMethodsWithoutStatusWrites = map[string]string{
	"WithTaskAdmission":               "admission gate around a caller's transition",
	"Enqueue":                         "creates a new job",
	"PrepareEnqueue":                  "fills fields before a job exists",
	"PublishEnqueued":                 "publishes an event for an existing row",
	"List":                            "read",
	"ListSessionPage":                 "read",
	"ListBySession":                   "read",
	"ListByWorkflowRunID":             "read",
	"ListCancellationRequestsByRunID": "read",
	"ListByWorkspacePath":             "read",
	"Get":                             "read",
	"GetLatestByChildSessionID":       "read",
	"SetChildSessionID":               "binds the child session only",
	"PublishWorkerProgress":           "progress snapshot only",
	"RenewClaim":                      "extends a running lease only",
	"ListPendingOutcomes":             "read",
	"MarkOutcomeDelivered":            "delivery ledger only",
	"ClaimWorkerBranch":               "branch provisioning only",
	"EnsureWorkerBranch":              "branch provisioning only",
	"ListBranchJobs":                  "read",
}

func TestQueueStatusTransitionsCoverWorkerQueue(t *testing.T) {
	queueType := reflect.TypeFor[worker.WorkerQueue]()
	for i := range queueType.NumMethod() {
		name := queueType.Method(i).Name
		_, transition := queueStatusTransitions[name]
		_, exempt := queueMethodsWithoutStatusWrites[name]
		if transition == exempt {
			t.Errorf("WorkerQueue.%s must be exactly one of a status transition or a reasoned exemption", name)
		}
	}
	for name := range queueStatusTransitions {
		if _, ok := queueType.MethodByName(name); !ok {
			t.Errorf("status transition %s is not a WorkerQueue method", name)
		}
	}
	for name := range queueMethodsWithoutStatusWrites {
		if _, ok := queueType.MethodByName(name); !ok {
			t.Errorf("exemption %s is not a WorkerQueue method", name)
		}
	}
}

// seedTaskWithStatus brings one job into status through queue transitions.
func seedTaskWithStatus(t *testing.T, backend string, status api.WorkerStatus) (worker.WorkerQueue, string) {
	t.Helper()
	ctx := t.Context()
	queue := matrixQueueBackends[backend](t)
	jobID, err := queue.Enqueue(ctx, api.WorkerTask{
		ParentSessionID: "parent-1",
		ProjectID:       testdbseed.DefaultProjectID,
		AgentType:       "implementer",
		Prompt:          "matrix task",
		Brief:           "fixture",
		ExecutionTarget: api.ExecutionTargetLocal,
	})
	testutil.FailErr(t, "enqueue", err)
	testutil.FailErr(t, "bind child", queue.SetChildSessionID(ctx, jobID, "child-1"))

	claim := func() *api.WorkerTask {
		claimed, err := queue.ClaimNext(ctx, matrixClaim)
		testutil.FailErr(t, "claim", err)
		return claimed
	}
	settle := func(step string, won bool, err error) {
		testutil.FailErr(t, step, err)
		if !won {
			t.Fatalf("%s lost on a freshly claimed job", step)
		}
	}
	switch status {
	case api.WorkerStatusPending:
	case api.WorkerStatusHeld:
		testutil.FailErr(t, "hold", queue.Hold(ctx, jobID))
	case api.WorkerStatusRunning:
		claim()
	case api.WorkerStatusWaiting:
		won, err := queue.Park(ctx, claim())
		settle("park", won, err)
	case api.WorkerStatusComplete:
		won, err := queue.Complete(ctx, claim(), api.WorkerResult{Status: "complete"})
		settle("complete", won, err)
	case api.WorkerStatusFailed:
		won, err := queue.Fail(ctx, claim(), errors.New("seeded failure"))
		settle("fail", won, err)
	case api.WorkerStatusCanceled:
		testutil.FailErr(t, "cancel", queue.Cancel(ctx, jobID, &api.WorkerResult{Status: "canceled"}))
	default:
		t.Fatalf("no seeding path for worker status %s", status)
	}
	if task := matrixTask(t, queue, jobID); task.Status != status {
		t.Fatalf("seeded status=%s want %s", task.Status, status)
	}
	return queue, jobID
}

func matrixTask(t *testing.T, queue worker.WorkerQueue, jobID string) *api.WorkerTask {
	t.Helper()
	task, ok := queue.Get(jobID)
	if !ok || task == nil {
		t.Fatalf("task %s not found", jobID)
	}
	return task
}

func TestTerminalWorkerStatusesRejectEveryTransition(t *testing.T) {
	for backend := range matrixQueueBackends {
		for _, status := range api.AllWorkerStatuses() {
			if !status.IsTerminal() {
				continue
			}
			for method, transition := range queueStatusTransitions {
				t.Run(backend+"/"+string(status)+"/"+method, func(t *testing.T) {
					queue, jobID := seedTaskWithStatus(t, backend, status)
					won, _ := transition(t.Context(), queue, jobID, matrixTask(t, queue, jobID))
					if won {
						t.Fatalf("%s won on terminal status %s", method, status)
					}
					if got := matrixTask(t, queue, jobID).Status; got != status {
						t.Fatalf("%s moved terminal status %s to %s", method, status, got)
					}
				})
			}
		}
	}
}

func TestNonTerminalWorkerStatusesCancel(t *testing.T) {
	for backend := range matrixQueueBackends {
		for _, status := range api.AllWorkerStatuses() {
			if status.IsTerminal() {
				continue
			}
			t.Run(backend+"/"+string(status), func(t *testing.T) {
				queue, jobID := seedTaskWithStatus(t, backend, status)
				testutil.FailErr(t, "cancel", queue.Cancel(t.Context(), jobID, &api.WorkerResult{Status: "canceled"}))
				if got := matrixTask(t, queue, jobID).Status; got != api.WorkerStatusCanceled {
					t.Fatalf("cancel left %s at %s", status, got)
				}
			})
		}
	}
}
